package projects

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	restclient "k8s.io/client-go/rest"
	kclientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	kclientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/kubectl/cmd/templates"
	kcmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions"

	oapi "github.com/openshift/origin/pkg/api"
	clientcfg "github.com/openshift/origin/pkg/client/config"
	ocproject "github.com/openshift/origin/pkg/oc/cli/project"
	cliconfig "github.com/openshift/origin/pkg/oc/lib/kubeconfig"
	projectapi "github.com/openshift/origin/pkg/project/apis/project"
	projectapihelpers "github.com/openshift/origin/pkg/project/apis/project/helpers"
	projectclientinternal "github.com/openshift/origin/pkg/project/generated/internalclientset"
	projectclient "github.com/openshift/origin/pkg/project/generated/internalclientset/typed/project/internalversion"
)

type ProjectsOptions struct {
	Config       clientcmdapi.Config
	ClientConfig *restclient.Config
	Client       projectclient.ProjectInterface
	KubeClient   kclientset.Interface
	PathOptions  *kclientcmd.PathOptions

	// internal strings
	CommandName string

	DisplayShort bool

	genericclioptions.IOStreams
}

func NewProjectsOptions(name string, streams genericclioptions.IOStreams) *ProjectsOptions {
	return &ProjectsOptions{
		IOStreams:   streams,
		CommandName: name,
	}
}

// SortByProjectName is sort
type SortByProjectName []projectapi.Project

func (p SortByProjectName) Len() int {
	return len(p)
}
func (p SortByProjectName) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}
func (p SortByProjectName) Less(i, j int) bool {
	return p[i].Name < p[j].Name
}

var (
	projectsLong = templates.LongDesc(`
		Display information about the current active project and existing projects on the server.

		For advanced configuration, or to manage the contents of your config file, use the 'config'
		command.`)
)

// NewCmdProjects implements the OpenShift cli rollback command
func NewCmdProjects(fullName string, f kcmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := NewProjectsOptions(fullName, streams)

	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Display existing projects",
		Long:  projectsLong,
		Run: func(cmd *cobra.Command, args []string) {
			if err := o.Complete(f, cmd, args); err != nil {
				kcmdutil.CheckErr(kcmdutil.UsageErrorf(cmd, err.Error()))
			}
			kcmdutil.CheckErr(o.Validate(args))
			kcmdutil.CheckErr(o.RunProjects())
		},
	}

	cmd.Flags().BoolVarP(&o.DisplayShort, "short", "q", false, "If true, display only the project names")
	return cmd
}

func (o *ProjectsOptions) Complete(f kcmdutil.Factory, cmd *cobra.Command, args []string) error {
	o.PathOptions = cliconfig.NewPathOptions(cmd)

	var err error
	o.Config, err = f.ToRawKubeConfigLoader().RawConfig()
	if err != nil {
		return err
	}

	o.ClientConfig, err = f.ToRESTConfig()
	if err != nil {
		return err
	}

	o.KubeClient, err = f.ClientSet()
	if err != nil {
		return err
	}
	projectClient, err := projectclientinternal.NewForConfig(o.ClientConfig)
	if err != nil {
		return err
	}
	o.Client = projectClient.Project()

	return nil
}

func (o *ProjectsOptions) Validate(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("no arguments should be passed")
	}
	return nil
}

// RunProjects lists all projects a user belongs to
func (o ProjectsOptions) RunProjects() error {
	config := o.Config

	var currentProject string
	currentContext := config.Contexts[config.CurrentContext]
	if currentContext != nil {
		currentProject = currentContext.Namespace
	}

	var currentProjectExists bool
	var currentProjectErr error

	client := o.Client

	if len(currentProject) > 0 {
		if currentProjectErr := ocproject.ConfirmProjectAccess(currentProject, o.Client, o.KubeClient); currentProjectErr == nil {
			currentProjectExists = true
		}
	}

	var defaultContextName string
	if currentContext != nil {
		defaultContextName = clientcfg.GetContextNickname(currentContext.Namespace, currentContext.Cluster, currentContext.AuthInfo)
	}

	var msg string
	projects, err := ocproject.GetProjects(client, o.KubeClient)
	if err == nil {
		switch len(projects) {
		case 0:
			if !o.DisplayShort {
				msg += "You are not a member of any projects. You can request a project to be created with the 'new-project' command."
			}
		case 1:
			if o.DisplayShort {
				msg += fmt.Sprintf("%s", projects[0].Name)
			} else {
				msg += fmt.Sprintf("You have one project on this server: %q.", projectapihelpers.DisplayNameAndNameForProject(&projects[0]))
			}
		default:
			asterisk := ""
			count := 0
			if !o.DisplayShort {
				msg += fmt.Sprintf("You have access to the following projects and can switch between them with '%s project <projectname>':\n", o.CommandName)
			}

			sort.Sort(SortByProjectName(projects))
			for _, project := range projects {
				count = count + 1
				displayName := project.Annotations[oapi.OpenShiftDisplayName]
				linebreak := "\n"
				if len(displayName) == 0 {
					displayName = project.Annotations["displayName"]
				}

				if currentProjectExists && !o.DisplayShort {
					asterisk = "    "
					if currentProject == project.Name {
						asterisk = "  * "
					}
				}
				if len(displayName) > 0 && displayName != project.Name && !o.DisplayShort {
					msg += fmt.Sprintf("\n"+asterisk+"%s - %s", project.Name, displayName)
				} else {
					if o.DisplayShort && count == 1 {
						linebreak = ""
					}
					msg += fmt.Sprintf(linebreak+asterisk+"%s", project.Name)
				}
			}
		}
		fmt.Println(msg)

		if len(projects) > 0 && !o.DisplayShort {
			if !currentProjectExists {
				if kapierrors.IsForbidden(currentProjectErr) {
					fmt.Printf("You do not have rights to view project %q. Please switch to an existing one.\n", currentProject)
				}
				return currentProjectErr
			}

			// if they specified a project name and got a generated context, then only show the information they care about.  They won't recognize
			// a context name they didn't choose
			if config.CurrentContext == defaultContextName {
				fmt.Fprintf(o.Out, "\nUsing project %q on server %q.\n", currentProject, o.ClientConfig.Host)
			} else {
				fmt.Fprintf(o.Out, "\nUsing project %q from context named %q on server %q.\n", currentProject, config.CurrentContext, o.ClientConfig.Host)
			}
		}
		return nil
	}

	return err
}
