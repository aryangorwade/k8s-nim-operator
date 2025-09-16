### High-level overview
- **Purpose**: A kubectl plugin to manage NVIDIA NIM Operator custom resources on Kubernetes: `NIMService` and `NIMCache`.
- **Binary/entrypoint**: `kubectl-nim` with a root `nim` command that exposes subcommands: `get`, `status`, `logs`, `delete`, `deploy`.
- **Core libraries**:
  - **Cobra**: command line parsing.
  - **kubectl’s Factory**: integrates kubeconfig, REST config, and typed client creation.
  - **NIM Operator clientset**: typed API for `NIMService` and `NIMCache`.
  - **controller-runtime zap logger**: global structured logging.

Project tree (selected):
- `cmd/kubectl-nim.go`: binary entrypoint
- `pkg/cmd/`: root and subcommands
  - `nim.go`: root command factory and wiring
  - `get/`, `status/`, `log/`, `delete/`, `deploy/`: subcommand implementations
- `pkg/util/`: shared types, defaults, clients, and resource fetching helpers
- `scripts/`: embedded bash script used by `nim logs`

### Entrypoint: process startup → Cobra root → subcommands
- The binary configures Cobra with `genericiooptions.IOStreams` and executes the root command.

```12:21:cmd/kubectl-nim.go
func main() {
	flags := flag.NewFlagSet("kubectl-nim", flag.ExitOnError)
	flag.CommandLine = flags
	ioStreams := genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}

	root := cmd.NewNIMCommand(ioStreams)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- The root command:
  - Sets a global zap logger via controller-runtime and keeps help behavior as default `Run`.
  - Instantiates kubectl `ConfigFlags` and a `cmdutil.Factory`, hides kubeconfig flags from help, and wires subcommands.

```25:56:pkg/cmd/nim.go
func NewNIMCommand(streams genericiooptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "nim",
		Short:        "nim operator kubectl plugin",
		Long:         "Manage NIM Operator resources like NIMCache and NIMService on Kubernetes",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmd.HelpFunc()(cmd, args)
		},
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}

	configFlags := genericclioptions.NewConfigFlags(true)
	configFlags.AddFlags(cmd.PersistentFlags())

	cmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		_ = cmd.PersistentFlags().MarkHidden(f.Name)
	})

	cmdFactory := cmdutil.NewFactory(configFlags)

	cmd.AddCommand(get.NewGetCommand(cmdFactory, streams))
	cmd.AddCommand(status.NewStatusCommand(cmdFactory, streams))
	cmd.AddCommand(log.NewLogCommand(cmdFactory, streams))
	cmd.AddCommand(delete.NewDeleteCommand(cmdFactory, streams))
	cmd.AddCommand(deploy.NewDeployCommand(cmdFactory, streams))

	return cmd
}
```

### Utilities: shared types, defaults, client builder, and resource fetching
- **`pkg/util/types.go`**
  - Defines a small enum for resource kind selection across commands.

```1:8:pkg/util/types.go
type ResourceType string

const (
	NIMService ResourceType = "nimservice"
	NIMCache   ResourceType = "nimcache"
)
```

- **`pkg/util/constant.go`**
  - Collects common default values for flags across commands (PVCs, images, pull secrets, service configuration, scaling), and NIMCache-specific flags (model source, resource sizes, QoS, etc.). Defaults are centralized here to ensure consistency and to differentiate “not provided” vs “empty”.

- **Why defaults are centralized**:
  - Cobra flags need well-defined zero/default values. Some flags represent optional fields in CR specs (e.g., unset vs empty). Centralizing defaults avoids duplication and keeps UX consistent across commands.

- **`pkg/util/client/client.go`**
  - Wraps creation of both the Kubernetes core `Clientset` and the NIM Operator typed clientset from the shared `cmdutil.Factory`.
  - Exposes a `Client` interface with `KubernetesClient()` and `NIMClient()`; commands depend on this interface for testability and separation of concerns.

- **`pkg/util/fetch_resource.go`**
  - Shared options and helper to resolve namespace, parse arguments, and fetch CR lists via the typed clientset.
  - Used by `get`, `status`, `delete`, and namespace parsing for `logs`.

Key parts:
- Options struct (shared across read/describe/delete):

```21:28:pkg/util/fetch_resource.go
type FetchResourceOptions struct {
	cmdFactory    cmdutil.Factory
	IoStreams     *genericclioptions.IOStreams
	Namespace     string
	ResourceName  string
	ResourceType  ResourceType
	AllNamespaces bool
}
```

- Namespace/args completion:
  - Pulls `--namespace`; defaults to `default`.
  - If one positional arg is present, treats it as a resource name (for get/status).
  - If two args are present, validates the resource type and takes the second as name (used by delete).

```37:69:pkg/util/fetch_resource.go
func (options *FetchResourceOptions) CompleteNamespace(args []string, cmd *cobra.Command) error {
	...
	if len(args) == 1 {
		options.ResourceName = args[0]
	}
	if len(args) == 2 {
		resourceType := ResourceType(strings.ToLower(args[0]))
		switch resourceType {
		case NIMService, NIMCache:
			options.ResourceType = resourceType
		default:
			return fmt.Errorf("invalid resource type %q. Valid types are: nimservice, nimcache", args[0])
		}
		options.ResourceName = args[1]
	}
	return nil
}
```

- Fetch function:
  - Produces a `.List(...)` call with an optional field selector if a name is given.
  - Returns typed `NIMServiceList` or `NIMCacheList`.
  - Validates “not found” for name-constrained queries to provide good UX.

```71:151:pkg/util/fetch_resource.go
func FetchResources(ctx context.Context, options *FetchResourceOptions, k8sClient client.Client) (interface{}, error) {
	...
	switch options.ResourceType {
	case NIMService:
		...
		if options.AllNamespaces {
			resourceList, err = k8sClient.NIMClient().AppsV1alpha1().NIMServices("").List(ctx, listopts)
		} else {
			resourceList, err = k8sClient.NIMClient().AppsV1alpha1().NIMServices(options.Namespace).List(ctx, listopts)
		}
		...
	case NIMCache:
		...
		if options.AllNamespaces {
			resourceList, err = k8sClient.NIMClient().AppsV1alpha1().NIMCaches("").List(ctx, listopts)
		} else {
			resourceList, err = k8sClient.NIMClient().AppsV1alpha1().NIMCaches(options.Namespace).List(ctx, listopts)
		}
		...
	}
	return resourceList, nil
}
```

- Condition summarization:
  - `MessageCondition(...)` picks a condition to display: prioritizes `Failed` with message, then `Ready`, then first with non-empty message, otherwise the first condition.

### Subcommand: get
- Location: `pkg/cmd/get/`
- Command: `nim get`
  - Subcommands: `nim get nimservice [NAME] [-A]`, `nim get nimcache [NAME] [-A]`
- Flow:
  - Create `FetchResourceOptions`, bind `--all-namespaces`.
  - On `RunE`: complete namespace, create client, set `ResourceType`, call common `get.Run`.

```36:63:pkg/cmd/get/get.go
func Run(ctx context.Context, options *util.FetchResourceOptions, k8sClient client.Client) error {
	resourceList, err := util.FetchResources(ctx, options, k8sClient)
	if err != nil {
		return err
	}
	switch options.ResourceType {
	case util.NIMService:
		nimServiceList, ok := resourceList.(*appsv1alpha1.NIMServiceList)
		...
		return printNIMServices(nimServiceList, options.IoStreams.Out)
	case util.NIMCache:
		nimCacheList, ok := resourceList.(*appsv1alpha1.NIMCacheList)
		...
		return printNIMCaches(nimCacheList, options.IoStreams.Out)
	}
	return err
}
```

- `nim get nimservice` output:
  - Columns: Name, Namespace, Image, Expose Service, Replicas, Scale, Storage, Resources, State, Age.
  - Helpers:
    - `getExpose(...)`: formats service name/port if present.
    - `getScale(...)`: “disabled” or min/max replicas if HPA enabled.
    - `getStorage(...)`: displays NIMCache reference, PVC details, or HostPath.
    - `getNIMServiceResources(...)`: prints limits/requests/claims compactly.

- `nim get nimcache` output:
  - Columns: Name, Namespace, Source, Model/ModelPuller, CPU, Memory, PVC Volume, State, Age.
  - Helpers:
    - `getSource(...)`: NGC vs NeMo DataStore vs HuggingFace.
    - `getModel(...)`: depending on source, either model puller image, model name, or endpoint.
    - `getPVCDetails(...)`: PVC name + size or size.

### Subcommand: status
- Location: `pkg/cmd/status/`
- Command: `nim status`
  - Subcommands: `nim status nimservice [NAME] [-A]`, `nim status nimcache [NAME] [-A]`
- Flow mirrors `get`, but focuses on status fields and conditions via `util.MessageCondition`.

```36:67:pkg/cmd/status/status.go
func Run (ctx context.Context, options *util.FetchResourceOptions, k8sClient client.Client) error {
	resourceList, err := util.FetchResources(ctx, options, k8sClient)
	...
	switch options.ResourceType {
	case util.NIMService:
		nimServiceList := resourceList.(*appsv1alpha1.NIMServiceList)
		return printNIMServices(nimServiceList, options.IoStreams.Out)
	case util.NIMCache:
		nimCacheList := resourceList.(*appsv1alpha1.NIMCacheList)
		if options.ResourceName != "" && len(nimCacheList.Items) == 1 {
			return printSingleNIMCache(&nimCacheList.Items[0], options.IoStreams.Out)
		}
		return printNIMCaches(nimCacheList, options.IoStreams.Out)
	}
	return err
}
```

- `nim status nimservice` output:
  - Columns: Name, Namespace, State, Available Replicas, Type/Status, Last Transition Time, Message, Age.

- `nim status nimcache` output:
  - When a single named resource is requested and found, prints a multi-line paragraph:
    - Name, Namespace, State, PVC, Type/Status, Last Transition Time, Message, Age, and “Cached NIM Profiles” enumerated.
  - Otherwise, a table with columns similar to the NIMService status table but tailored to NIMCache (includes PVC).

### Subcommand: logs
- Location: `pkg/cmd/log/`
- Command: `nim logs collect [-n NAMESPACE]`
- Purpose: Collect a diagnostic bundle (must-gather style) for the operator, NIM services/caches, cluster storage, and optionally NeMo microservices.
- Architecture:
  - The bash script `scripts/must-gather.sh` is embedded at compile time via `//go:embed` in `scripts/embed.go`.
  - The command materializes the script to a temp file, marks it executable, and runs it with `OPERATOR_NAMESPACE=nim-operator` and `NIM_NAMESPACE=<namespace>` set.
  - Both stdout and stderr are captured (the script uses `set -x`), and `ARTIFACT_DIR=...` is parsed from either stream.
  - After script completion, the command lists the collected `.log` files under `<artifactDir>/nim` and prints their paths.

```61:101:pkg/cmd/log/log.go
func Run(ctx context.Context, options *util.FetchResourceOptions) error {
	tmp, err := os.CreateTemp("", "must-gather-*.sh")
	...
	if err := os.WriteFile(tmp.Name(), scripts.MustGather, 0o755); err != nil {
		return fmt.Errorf("write script: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/bash", tmp.Name())
	cmd.Env = append(os.Environ(),
		"OPERATOR_NAMESPACE=nim-operator",
		"NIM_NAMESPACE="+options.Namespace,
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("must-gather failed: %w\nstderr:\n%s", err, stderr.String())
	}
	artifactDir := parseArtifactDir(stdout.Bytes(), stderr.Bytes())
	...
	paths, err := listResourceLogPaths(artifactDir)
	...
	fmt.Printf("\nDiagnostic bundle created at  %s.\n", nimDir)
	fmt.Printf("Saved %d log file(s) in:\n", len(paths))
	for _, p := range paths {
		fmt.Printf("  %s\n", p)
	}
	return nil
}
```

- The script itself:
  - Validates `kubectl` or `oc` availability.
  - Requires `OPERATOR_NAMESPACE` and `NIM_NAMESPACE`.
  - Writes comprehensive cluster info, operator and workload resources, logs, and descriptions under a timestamped `ARTIFACT_DIR`.
  - Collects NIM CRs (`nimcaches`, `nimservices`, `nimpipelines`), their pods/ingress, and storage objects; optionally collects NeMo microservices if `NEMO_NAMESPACE` is set.

### Subcommand: delete
- Location: `pkg/cmd/delete/`
- Command: `nim delete (nimservice|nimcache) NAME [-n ...]`
- Flow:
  - Parses two positional arguments: resource type and name.
  - Uses `FetchResourceOptions` to resolve namespace and validate kind; then fetches the item list with a name selector.
  - If found, the command deletes via typed clientset with the namespace derived from the object (works even if `--namespace` didn’t match).
  - Prints a confirmation to `stdout`.

```68:109:pkg/cmd/delete/delete.go
func Run(ctx context.Context, options *util.FetchResourceOptions, k8sClient client.Client) error {
	resourceList, err := util.FetchResources(ctx, options, k8sClient)
	...
	switch options.ResourceType {
	case util.NIMService:
		nl, ok := resourceList.(*appsv1alpha1.NIMServiceList)
		...
		ns = nl.Items[0].Namespace
		if err := k8sClient.NIMClient().AppsV1alpha1().NIMServices(ns).Delete(ctx, name, v1.DeleteOptions{}); err != nil {
			return fmt.Errorf("failed to delete NIMService %s/%s: %w", ns, name, err)
		}
		fmt.Fprintf(options.IoStreams.Out, "NIMService %q deleted in namespace %q\n", name, ns)
	case util.NIMCache:
		...
	}
	return nil
}
```

- Rationale:
  - Using `FetchResources` first provides a uniform “not found” experience and dynamically discovers the live namespace of the resource before deletion.

### Subcommand: deploy
- Location: `pkg/cmd/deploy/`
- Command: `nim deploy`
  - Subcommands: `nim deploy nimservice NAME [flags]`, `nim deploy nimcache NAME [flags]`
- Design:
  - Each deploy subcommand defines a dedicated `Options` struct with all flags used to compose a CR spec and provide clean separation of concerns.
  - After `CompleteNamespace` and optional validation, the `Run` method builds the full CR object and creates it with the typed clientset.

#### Deploying NIMService
- Options structure:

```23:50:pkg/cmd/deploy/deploy_nimservice.go
type NIMServiceOptions struct {
	cmdFactory             cmdutil.Factory
	IoStreams              *genericclioptions.IOStreams
	Namespace              string
	ResourceName           string
	ResourceType           util.ResourceType
	AllNamespaces          bool
	ImageRepository        string
	Tag                    string
	NIMCacheStorageName    string
	NIMCacheStorageProfile string
	PVCCreate              bool
	PVCStorageName         string
	PVCStorageClass        string
	PVCSize                string
	PVCVolumeAccessMode    string
	PullPolicy             string
	PullSecrets            []string
	AuthSecret             string
	ServicePort            int32
	ServiceType            string
	GPULimit               string
	Replicas               int
	ScaleMaxReplicas       int32
	ScaleMinReplicas       int32
	InferencePlatform      string
	HostPath               string
}
```

- Why `NIMServiceOptions` exists:
  - Deploy requires many flags and non-trivial mapping to CR spec. Encapsulating in a struct:
    - Organizes flag parsing and defaults.
    - Allows dedicated “fill spec” function for clarity and testability.
    - Cleanly separates CLI parsing from API composition.

- `Run` flow:
  - `CompleteNamespace` sets namespace and name.
  - `FillOutNIMServiceSpec(options)` constructs the full `appsv1alpha1.NIMService` spec including:
    - Image repo/tag, pull policy, pull secrets.
    - Storage: one of NIMCache reference, PVC (existing or create), or HostPath (validated elsewhere by admission).
    - Service exposure: port and service type.
    - Resource requests: GPU limit quantity into `Resources.Limits["nvidia.com/gpu"]`.
    - Replicas and HPA/Scale: enable autoscaling if `ScaleMaxReplicas` is present; set min if provided.
    - Inference platform: `standalone` or `kserve`.
  - Creates the CR in the resolved namespace.

```144:164:pkg/cmd/deploy/deploy_nimservice.go
func RunDeployNIMService(ctx context.Context, options *NIMServiceOptions, k8sClient client.Client) error {
	nimservice, err := FillOutNIMServiceSpec(options)
	...
	nimservice.Name = options.ResourceName
	nimservice.Namespace = options.Namespace
	if _, err := k8sClient.NIMClient().AppsV1alpha1().NIMServices(options.Namespace).Create(ctx, nimservice, v1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create NIMService %s/%s: %w", options.Namespace, options.ResourceName, err)
	}
	fmt.Fprintf(options.IoStreams.Out, "NIMService %q created in namespace %q\n", options.ResourceName, options.Namespace)
	return nil
}
```

#### Deploying NIMCache
- Options structure:

```25:60:pkg/cmd/deploy/deploy_nimcache.go
type NIMCacheOptions struct {
	cmdFactory    cmdutil.Factory
	IoStreams     *genericclioptions.IOStreams
	Namespace     string
	ResourceName  string
	ResourceType  util.ResourceType
	AllNamespaces bool
	PVCCreate           bool
	PVCStorageName      string
	PVCStorageClass     string
	PVCSize             string
	PVCVolumeAccessMode string
	PullSecret string
	AuthSecret string
	SourceConfiguration string
	ResourcesCPU        string
	ResourcesMemory     string
	ModelPuller         string
	ModelEndpoint       string
	Precision           string
	Engine              string
	TensorParallelism   string
	QosProfile          string
	Lora                string
	Buildable           string
	Profiles            []string
	GPUs                []string
	AltEndpoint         string
	AltNamespace        string
	ModelName           string
	DatasetName         string
	Revision            string
}
```

- Why `NIMCacheOptions` exists:
  - Supports multiple “sources” (`ngc`, `huggingface`, `nemodatastore`) each with overlapping but distinct fields (auth, puller, profiles, GPU targets, model identity, revision, etc.). The struct cleanly aggregates input and isolates the mapping logic.

- Validation:
  - `Validate(options)` ensures `--nim-source` is one of the supported values to disambiguate which fields to set when constructing the `Spec.Source`.

- `Run` flow:
  - Build the full `appsv1alpha1.NIMCache` via `FillOutNIMCacheSpec(options)`:
    - `Spec.Source`:
      - `ngc`: sets `AuthSecret`, `ModelPuller`, `PullSecret`, optional `ModelEndpoint`, and `Model` sub-fields (profiles, precision, engine, tensor parallelism, QoS, optional booleans `Lora`, `Buildable`) and optional GPU product list.
      - `huggingface`: sets endpoint/namespace, optional model/dataset, `AuthSecret`, `ModelPuller`, `PullSecret`, optional revision.
      - `nemodatastore`: same shape as huggingface, but under `DataStore` fields.
    - Resources: CPU and Memory as `resource.Quantity`.
    - Storage: PVC fields including “create” semantics and volume access mode validation.
  - Create the CR in the namespace.

```175:195:pkg/cmd/deploy/deploy_nimcache.go
func RunDeployNIMCache(ctx context.Context, options *NIMCacheOptions, k8sClient client.Client) error {
	nimcache, err := FillOutNIMCacheSpec(options)
	...
	nimcache.Name = options.ResourceName
	nimcache.Namespace = options.Namespace
	if _, err := k8sClient.NIMClient().AppsV1alpha1().NIMCaches(options.Namespace).Create(ctx, nimcache, v1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create NIMCache %s/%s: %w", options.Namespace, options.ResourceName, err)
	}
	fmt.Fprintf(options.IoStreams.Out, "NIMCache %q created in namespace %q\n", options.ResourceName, options.Namespace)
	return nil
}
```

### Why the various Options structs exist
- **Encapsulation of CLI params**: Each command has a natural grouping of inputs (flags, positional args). Options structs keep these cohesive.
- **Separation of concerns**: Parsing flags vs. composing Kubernetes CRs are separate problems. Options act as the boundary, enabling clean “fill spec” functions.
- **Testability**: Functions like `FillOutNIMServiceSpec` and `FillOutNIMCacheSpec` can be unit-tested by instantiating options with different combinations.
- **Defaults and null semantics**: Combined with `pkg/util/constant.go`, Options distinguish between unset and deliberately-empty values, which matters for optional CR fields.

### How “Run” functions work end-to-end
- For “reader” commands (`get`, `status`):
  1. Build `FetchResourceOptions` + bind flags.
  2. `CompleteNamespace(args, cmd)`: resolve namespace; optionally set name; validate type if needed.
  3. Build `client.Client` via factory.
  4. Set `ResourceType` and call common `Run`.
  5. `Run` invokes `util.FetchResources` which lists typed CRs with an optional field selector by name.
  6. Cast the result to the specific list type and print with formatters.

- For `delete`:
  1. Parse `(nimservice|nimcache) NAME`.
  2. `CompleteNamespace` + `ResourceType` assignment.
  3. Fetch list by name via `FetchResources` to validate existence and discover actual namespace.
  4. Call `Delete(...)` on the typed clientset, then print confirmation.

- For `deploy`:
  1. Per-kind options struct is populated from flags.
  2. `CompleteNamespace` sets namespace and resource name.
  3. Build CR spec via a dedicated “FillOutSpec” function.
  4. Create the resource in the cluster via typed clientset, then print confirmation.

- For `logs`:
  1. Namespace is parsed through the shared `FetchResourceOptions`.
  2. Embedded script is written to a temp file with execute permissions and invoked with the proper env vars.
  3. Output parsing extracts `ARTIFACT_DIR`, and the command then prints the resulting log bundle contents.

### Notes on behavior and UX
- **Kubeconfig flags hidden**: Although the CLI inherits `kubectl`’s kubeconfig flags, they are hidden in help for a clean UX. They continue to work via Cobra’s persistent flags.
- **All-namespaces support**: `get` and `status` support `-A/--all-namespaces`. Name lookups use a field selector on `.metadata.name`, so uniqueness across namespaces is handled by the caller’s selection.
- **Cross-namespace delete**: `delete` discovers the live namespace of the named resource, allowing users to delete without perfectly specifying `-n`.
- **Condition messaging**: `status` picks a meaningful condition (prefer `Failed` with a message) to surface user-actionable context.

### External APIs and types
- The CLI consumes CRDs from the NIM operator via `github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1`.
- It creates a typed client using `github.com/NVIDIA/k8s-nim-operator/api/versioned` and the REST config from the `cmdutil.Factory`.

### Typical usage
- Get:
  - `nim get nimservice` or `nim get nimservice NAME`
  - `nim get nimcache -A`
- Status:
  - `nim status nimcache my-cache`
  - `nim status nimservice -n ns`
- Logs:
  - `nim logs collect -n ns`
- Delete:
  - `nim delete nimservice my-svc -n ns`
  - `nim delete nimcache my-cache`
- Deploy:
  - NIMService (PVC existing): `nim deploy nimservice svc --image-repository=... --tag=... --pvc-storage-name=...`
  - NIMService (create PVC): `nim deploy nimservice svc --image-repository=... --tag=... --pvc-create=true --pvc-size=... --pvc-volume-access-mode=... --pvc-storage-class=...`
  - NIMService (NIMCache storage): `nim deploy nimservice svc --image-repository=... --tag=... --nimcache-storage-name=<nimcache>`
  - NIMCache (NGC): `nim deploy nimcache cache --nim-source=ngc --model-puller=... --auth-secret=... [--profiles=...] [--gpus=...]`
  - NIMCache (HF/NeMo DataStore): `nim deploy nimcache cache --nim-source=huggingface --alt-endpoint=... --alt-namespace=... --auth-secret=... --model-puller=... --pull-secret=...`

- The CLI is a `kubectl` plugin with a Cobra root in `pkg/cmd/nim.go`, and entrypoint `cmd/kubectl-nim.go`.
- Shared utilities in `pkg/util/` provide: defaults, resource-kind enums, a dual clientset builder, and a common resource fetcher with namespace/arg parsing.
- Each subcommand follows a predictable pattern: complete options → build client → fetch or compose CRs → print or create/delete.
- Deploy subcommands rely on dedicated `Options` structs to capture flags and map them directly into `NIMService`/`NIMCache` specs, then create CRs via the typed NIM client.
