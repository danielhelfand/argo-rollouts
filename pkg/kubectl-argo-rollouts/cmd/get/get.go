package get

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/kubectl/pkg/cmd/get"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/pkg/kubectl-argo-rollouts/info"
	"github.com/argoproj/argo-rollouts/pkg/kubectl-argo-rollouts/options"
)

var (
	colorMapping = map[string]int{
		// Colors for icons
		info.IconWaiting:     FgYellow,
		info.IconProgressing: FgHiBlue,
		info.IconWarning:     FgRed,
		info.IconUnknown:     FgYellow,
		info.IconOK:          FgGreen,
		info.IconBad:         FgRed,
		//info.IconPaused:      FgWhite,
		//info.IconNeutral:     FgWhite, // (foreground is better than white)

		// Colors for canary/stable/preview tags
		info.InfoTagCanary:  FgYellow,
		info.InfoTagStable:  FgGreen,
		info.InfoTagActive:  FgGreen,
		info.InfoTagPreview: FgHiBlue,
		info.InfoTagPing:    FgHiBlue,
		info.InfoTagPong:    FgHiBlue,

		// Colors for highlighting experiment/analysisruns
		string(v1alpha1.AnalysisPhasePending): FgHiBlue,
		string(v1alpha1.AnalysisPhaseRunning): FgHiBlue,
	}
)

const (
	IconRollout    = "⟳"
	IconRevision   = "#"
	IconReplicaSet = "⧉"
	IconPod        = "□"
	IconJob        = "⊞"
	IconService    = "⑃" // other options: ⋲ ⇶ ⋔ ⤨
	IconExperiment = "Σ" // other options: ꀀ ⋃ ⨄
	IconAnalysis   = "α" // other options: ⚯
)

// ANSI escape codes
const (
	escape    = "\x1b"
	noFormat  = 0
	Bold      = 1
	FgBlack   = 30
	FgRed     = 31
	FgGreen   = 32
	FgYellow  = 33
	FgBlue    = 34
	FgMagenta = 35
	FgCyan    = 36
	FgWhite   = 37
	FgDefault = 39
	FgHiBlue  = 94
)

const (
	getExample = `
	# Get a rollout
	%[1]s get rollout guestbook

	# Watch a rollouts progress
	%[1]s get rollout guestbook -w
  
	# Get an experiment
	%[1]s get experiment my-experiment`

	getUsage = `This command consists of multiple subcommands which can be used to get extended information about a rollout or experiment.`

	getUsageCommon = `It returns a bunch of metadata on a resource and a tree view of the child resources created by the parent.
	
Tree view icons

| Icon | Kind |
|:----:|:-----------:|
| ⟳ | Rollout |
| Σ | Experiment |
| α | AnalysisRun |
| # | Revision |
| ⧉ | ReplicaSet |
| □ | Pod |
| ⊞ | Job |`
)

type GetOptions struct {
	Watch          bool
	NoColor        bool
	TimeoutSeconds int

	options.ArgoRolloutsOptions
}

// NewCmdGet returns a new instance of an `rollouts get` command
func NewCmdGet(o *options.ArgoRolloutsOptions) *cobra.Command {
	var cmd = &cobra.Command{
		Use:               "get <rollout|experiment> RESOURCE_NAME",
		Short:             "Get details about rollouts and experiments",
		Long:              getUsage,
		Example:           o.Example(getExample),
		SilenceUsage:      true,
		ValidArgsFunction: RolloutResourceNameCompletionFunc(cmdutil.NewFactory(o.ConfigFlags)),
		RunE: func(c *cobra.Command, args []string) error {
			return o.UsageErr(c)
		},
	}
	cmd.AddCommand(NewCmdGetRollout(o))
	cmd.AddCommand(NewCmdGetExperiment(o))
	return cmd
}

const (
	tableFormat = "%-17s%v\n"
)

func (o *GetOptions) PrintHeader(w io.Writer) {
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", "NAME", "KIND", "STATUS", "AGE", "INFO")
}

// Clear clears the terminal for updates for live watching of objects
func (o *GetOptions) Clear() {
	fmt.Fprint(o.Out, "\033[H\033[2J")
	fmt.Fprint(o.Out, "\033[0;0H")
}

// colorize adds ansi color codes to the string based on well known words
func (o *GetOptions) colorize(s string) string {
	if o.NoColor {
		return s
	}
	color := colorMapping[s]
	return o.ansiFormat(s, color)
}

// colorizeStatus adds ansi color codes to the string based on supplied status string
func (o *GetOptions) colorizeStatus(s string, status string) string {
	if o.NoColor {
		return s
	}
	color := colorMapping[status]
	return o.ansiFormat(s, color)
}

// ansiFormat wraps ANSI escape codes to a string to format the string to a desired color.
// NOTE: we still apply formatting even if there is no color formatting desired.
// The purpose of doing this is because when we apply ANSI color escape sequences to our
// output, this confuses the tabwriter library which miscalculates widths of columns and
// misaligns columns. By always applying a ANSI escape sequence (even when we don't want
// color, it provides more consistent string lengths so that tabwriter can calculate
// widths correctly.
func (o *GetOptions) ansiFormat(s string, codes ...int) string {
	if o.NoColor || os.Getenv("TERM") == "dumb" || len(codes) == 0 {
		return s
	}
	codeStrs := make([]string, len(codes))
	for i, code := range codes {
		codeStrs[i] = strconv.Itoa(code)
	}
	sequence := strings.Join(codeStrs, ";")
	return fmt.Sprintf("%s[%sm%s%s[%dm", escape, sequence, s, escape, noFormat)
}

// returns an appropriate tree prefix characters depending on whether or not the element is the
// last element of a list
func getPrefixes(isLast bool, subPrefix string) (string, string) {
	var childPrefix, childSubpfx string
	if !isLast {
		childPrefix = subPrefix + "├──"
		childSubpfx = subPrefix + "│  "
	} else {
		childPrefix = subPrefix + "└──"
		childSubpfx = subPrefix + "   "
	}
	return childPrefix, childSubpfx
}

func RolloutResourceNameCompletionFunc(f cmdutil.Factory) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var comps []string
		directive := cobra.ShellCompDirectiveNoFileComp
		if len(args) == 0 {
			comps, directive = doRolloutResourceCompletion(f, cmd, toComplete)
		}
		return comps, directive
	}
}

func doRolloutResourceCompletion(f cmdutil.Factory, cmd *cobra.Command, toComplete string) ([]string, cobra.ShellCompDirective) {
	var comps []string
	directive := cobra.ShellCompDirectiveNoFileComp
	slashIdx := strings.Index(toComplete, "/")
	if slashIdx == -1 {
		// Standard case, complete pod names
		comps = CompGetResource(f, cmd, "rollout", toComplete)

		// Also include resource choices for the <type>/<name> form,
		// but only for resources that contain pods
		resourcesWithPods := []string{"rollouts"}

		if len(comps) == 0 {
			// If there are no pods to complete, we will only be completing
			// <type>/.  We should disable adding a space after the /.
			directive |= cobra.ShellCompDirectiveNoSpace
		}

		for _, resource := range resourcesWithPods {
			if strings.HasPrefix(resource, toComplete) {
				comps = append(comps, fmt.Sprintf("%s/", resource))
			}
		}
	} else {
		// Dealing with the <type>/<name> form, use the specified resource type
		resourceType := toComplete[:slashIdx]
		toComplete = toComplete[slashIdx+1:]
		nameComps := CompGetResource(f, cmd, resourceType, toComplete)
		for _, c := range nameComps {
			comps = append(comps, fmt.Sprintf("%s/%s", resourceType, c))
		}
	}
	return comps, directive
}

func CompGetResource(f cmdutil.Factory, cmd *cobra.Command, resourceName string, toComplete string) []string {
	template := "{{ range .items  }}{{ .metadata.name }} {{ end }}"
	return CompGetFromTemplate(&template, f, "", cmd, []string{resourceName}, toComplete)
}

// CompGetFromTemplate executes a Get operation using the specified template and args and returns the results
// which begin with `toComplete`.
func CompGetFromTemplate(template *string, f cmdutil.Factory, namespace string, cmd *cobra.Command, args []string, toComplete string) []string {
	buf := new(bytes.Buffer)
	streams := genericclioptions.IOStreams{In: os.Stdin, Out: buf, ErrOut: ioutil.Discard}
	o := get.NewGetOptions("kubectl", streams)

	// Get the list of names of the specified resource
	o.PrintFlags.TemplateFlags.GoTemplatePrintFlags.TemplateArgument = template
	format := "go-template"
	o.PrintFlags.OutputFormat = &format

	// Do the steps Complete() would have done.
	// We cannot actually call Complete() or Validate() as these function check for
	// the presence of flags, which, in our case won't be there
	if namespace != "" {
		o.Namespace = namespace
		o.ExplicitNamespace = true
	} else {
		var err error
		o.Namespace, o.ExplicitNamespace, err = f.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			return nil
		}
	}

	o.ToPrinter = func(mapping *meta.RESTMapping, outputObjects *bool, withNamespace bool, withKind bool) (printers.ResourcePrinterFunc, error) {
		printer, err := o.PrintFlags.ToPrinter()
		if err != nil {
			return nil, err
		}
		return printer.PrintObj, nil
	}

	o.Run(f, cmd, args)

	var comps []string
	resources := strings.Split(buf.String(), " ")
	for _, res := range resources {
		if res != "" && strings.HasPrefix(res, toComplete) {
			comps = append(comps, res)
		}
	}
	return comps
}
