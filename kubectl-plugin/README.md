## Command tree and execution flows

### Root command: `nim`

- Hidden kube flags still work (e.g., `--kubeconfig`, `--context`, etc.) but are not shown in help text.
- Subcommands:
  - `nim get`
  - `nim status`
  - `nim logs collect`
  - `nim delete`
  - `nim deploy`

Each subcommand follows a consistent pattern:
1. Construct an Options struct and bind flags.
2. Resolve namespace/positional args (`CompleteNamespace` or equivalent).
3. Create the `client.Client` via the kubectl `Factory` (if needed).
4. Set `ResourceType` when applicable.
5. Invoke a `Run` function that either:
   - fetches and prints resources (for `get`/`status`), or
   - creates/deletes resources (for `deploy`/`delete`), or
   - runs diagnostics (for `logs`).

---

## Subcommand: get

- Location: `pkg/cmd/get/`
- Purpose: print concise tables summarizing `NIMService` or `NIMCache`.
- Usage:
  - `nim get nimservice [NAME] [-n NAMESPACE] [-A]`
  - `nim get nimcache [NAME] [-n NAMESPACE] [-A]`
- Flags:
  - `--all-namespaces, -A`: search across all namespaces (ignores `--namespace`).
- Flow:
  - Build `FetchResourceOptions`; set `ResourceType`; call a common `Run` that calls `util.FetchResources`.
  - Cast the returned list to the requested type and print a table.

Output:
- For `nimservice`:
  - Columns: Name, Namespace, Image, Expose Service, Replicas, Scale, Storage, Resources, State, Age.
  - Helpers interpret `Spec.Image`, `Spec.Expose`, `Spec.Storage`, `Spec.Resources`, `Spec.Scale enabled/HPA` and `Status.State`.
- For `nimcache`:
  - Columns: Name, Namespace, Source, Model/ModelPuller, CPU, Memory, PVC Volume, State, Age.
  - Helpers interpret the `Spec.Source.*` shape and `Spec.Resources`, and derive a human readable key (e.g., HF model name vs endpoint).

Why it’s split:
- Each resource type has dedicated printer and field summarization logic; reusing `FetchResourceOptions` keeps discovery logic uniform.

---

## Subcommand: status

- Location: `pkg/cmd/status/`
- Purpose: focus on conditions/status rather than spec summaries.
- Usage:
  - `nim status nimservice [NAME] [-n NAMESPACE] [-A]`
  - `nim status nimcache [NAME] [-n NAMESPACE] [-A]`
- Flow mirrors `get` but prints:
  - For `nimservice`: Name, Namespace, State, Available Replicas, Type/Status (Condition-Type/Status), Last Transition Time, Message, Age.
  - For `nimcache`:
    - If a single named resource is requested and found: prints a detailed paragraph with name, namespace, state, PVC, a chosen condition, age, and a list of cached NIM profiles (from status).
    - Otherwise: prints a table similar to `nimservice` but tailored to NIMCache (includes PVC).

Key logic:
- Uses `util.MessageCondition` to select a meaningful condition (prefer `Failed` with a non-empty message) for concise, actionable output.

---

## Subcommand: logs

- Location: `pkg/cmd/log/`
- Purpose: collect a must-gather style diagnostic bundle.
- Usage:
  - `nim logs collect [-n NAMESPACE]`
- Behavior:
  - Embeds `scripts/must-gather.sh` at build time (via `//go:embed` in `scripts/embed.go`).
  - On run:
    - Writes the script to a temp file with `0755` and executes it with:
      - `OPERATOR_NAMESPACE=nim-operator`
      - `NIM_NAMESPACE=<namespace from flags>`
    - Captures both stdout and stderr (script uses `set -x`).
    - Parses `ARTIFACT_DIR=...` from the output.
    - Lists `*.log` files under `<ARTIFACT_DIR>/nim` and prints their paths.

The script collects:
- Cluster version and GPU node info, storage classes/PVs/PVCs.
- Operator pod logs/descriptions in `OPERATOR_NAMESPACE`.
- NIM CRs (`nimservices`, `nimcaches`, `nimpipelines`) and associated pod logs/descriptions/ingress in `NIM_NAMESPACE`.
- Optionally, NeMo microservices if `NEMO_NAMESPACE` is set.

---

## Subcommand: delete

- Location: `pkg/cmd/delete/`
- Purpose: delete a named `NIMService` or `NIMCache`.
- Usage:
  - `nim delete nimservice NAME [-n NAMESPACE]`
  - `nim delete nimcache NAME [-n NAMESPACE]`
- Flow:
  - Parses `RESOURCE_TYPE` and `RESOURCE_NAME`.
  - `CompleteNamespace` validates resource type and sets name.
  - Calls `util.FetchResources` with a name field selector to validate existence and discover the resource’s actual namespace.
  - Calls typed client `Delete(...)` in that namespace.
  - Prints a human-readable confirmation.

Notes:
- Works even if `--namespace` doesn’t match; the discovered namespace from the live object is used.

---

## Subcommand: deploy

- Location: `pkg/cmd/deploy/`
- Purpose: create new CRs (`NIMService` or `NIMCache`) by mapping flags to CR spec fields.

Why dedicated `Options` structs exist here:
- There are many flags; mixing them into shared structs would reduce clarity.
- Keeps CLI parsing, defaulting, and CR spec composition cohesive and testable.
- Improves separation of concerns (flag parsing vs spec building vs API calls).

### Deploy `nimservice`

- Usage:
  - `nim deploy nimservice NAME [flags]`
- Required by design:
  - Must specify an image (`--image-repository`, `--tag`) and storage.
  - Storage can be one of:
    - Reference an existing `NIMCache` (`--nimcache-storage-name`).
    - PVC (existing: `--pvc-storage-name`, or create: `--pvc-create=true` plus size, access mode, and storage class).
    - HostPath (via `--host-path`, validated by admission).
- Notable flags and mapping:
  - Image: `--image-repository`, `--tag`, `--pull-policy`, `--pull-secrets`.
  - Auth: `--auth-secret`.
  - Service: `--service-port`, `--service-type` (ClusterIP/NodePort/LoadBalancer).
  - Resources: `--gpu-limit` parsed into `Resources.Limits["nvidia.com/gpu"]`.
  - Replicas: `--replicas`.
  - Autoscaling:
    - `--scale-max-replicas` enables autoscaling when provided (non-default).
    - Optional `--scale-min-replicas` if you want to set HPA minimum.
  - Inference platform: `--inference-platform` is one of `standalone` (default) or `kserve`.

- Execution:
  - `CompleteNamespace` sets `Namespace` and `ResourceName`.
  - `FillOutNIMServiceSpec(options)` populates `appsv1alpha1.NIMService.Spec` using flags:
    - Image spec, storage, auth, pull settings.
    - Service exposure (port/type).
    - Resource limits (GPU), replicas.
    - HPA fields when autoscaling enabled.
    - Inference platform enum.
  - Typed client `Create(...)` is called with the final CR object.

### Deploy `nimcache`

- Usage:
  - `nim deploy nimcache NAME [flags]`
- `--nim-source` (required) determines which source subsection is set in `Spec.Source`:
  - `ngc`
  - `huggingface`
  - `nemodatastore`
- Validation:
  - `Validate(options)` ensures `--nim-source` is one of the supported values so the code knows which sub-struct to fill.

- Flags and mapping (selected):
  - Common to sources:
    - Auth/pull: `--auth-secret`, `--model-puller`, `--pull-secret`.
    - Optional model selectors: `--profiles`, `--gpus` (GPU product names).
  - NGC:
    - `--ngc-model-endpoint` (optional).
    - Model subfields: `--precision`, `--engine`, `--tensor-parallelism`, `--qos-profile`.
    - `--lora`, `--buildable` are string booleans parsed to pointers.
  - HuggingFace / NeMo DataStore:
    - Alternate endpoint/namespace: `--alt-endpoint`, `--alt-namespace`.
    - Object identity: `--model-name`, `--dataset-name`, `--revision`.
    - Same auth/puller/pull-secret as above.
  - Resources for caching job:
    - `--resources-cpu`, `--resources-memory`.
  - Storage (PVC):
    - Name for existing PVC (`--pvc-storage-name`), or creation fields:
      - `--pvc-create=true`, `--pvc-size`, `--pvc-volume-access-mode`, `--pvc-storage-class`.

- Execution:
  - `CompleteNamespace` sets `Namespace` and `ResourceName`.
  - `FillOutNIMCacheSpec(options)`:
    - Sets the correct source sub-struct:
      - NGC: auth/puller/pull-secret, optional endpoint, full `Model` block including QoS/precision/engine/TPU/GPUs/Lora/Buildable.
      - HF/DataStore: endpoint/namespace, optional model/dataset/revision; auth/puller/pull-secret.
    - Parses resource quantities for CPU/Memory.
    - PVC fields + “create” semantics and access mode validation.
  - Typed client `Create(...)` with the final CR.

---

## Execution and error handling patterns

- Kube flags are hidden in help but are present as persistent flags; users may still pass `--kubeconfig`, `--context`, etc.
- Namespaces:
  - Defaults to `default` unless `--namespace` is provided.
  - `--all-namespaces` is supported for read-only operations.
- Lookup by name:
  - Uses a `.List` with field selector on `metadata.name` for precise matching.
- Condition selection:
  - Status commands pick the most useful condition (`Failed` with message > `Ready` > any with message > first) to surface actionable messages.
- Deletion:
  - Always fetch before delete to discover the resource’s actual namespace and provide consistent “not found” errors.
- Logs:
  - The diagnostic script is executed in a subprocess with both streams captured; any failure includes stderr for easier triage.

---

## Scripts

- `scripts/embed.go`: compile-time embeds `must-gather.sh` (Go 1.16+ `embed`).
- `scripts/must-gather.sh`: the collection logic:
  - Requires `OPERATOR_NAMESPACE` and `NIM_NAMESPACE`.
  - Collects cluster, storage, operator, and workload data into `ARTIFACT_DIR` (default `/tmp/nim_log_disagnostic_bundle_<ts>`).
  - Uses `kubectl` or `oc` if `kubectl` is unavailable.

---

## Tests (brief overview)

- `tests/` and subcommand-specific `*_test.go` files under `pkg/cmd/*` verify:
  - Command wiring and help behavior.
  - Flag parsing.
  - Output formatting for `get`/`status`.
  - `logs` command integration.
  - `delete`/`deploy` behavior and validation surfaces.

---

## Extensibility guidelines

- Adding a subcommand:
  - Create `pkg/cmd/<name>/` with `New<Name>Command(...)` and one or more `RunE` handlers.
  - If it fetches CRs, reuse `util.FetchResourceOptions` and `util.FetchResources`.
  - If it creates CRs, define a dedicated `Options` struct and a `FillOut<CR>Spec` function.
  - Register it in `pkg/cmd/nim.go`.

- Adding new flags:
  - Add defaults to `pkg/util/constant.go` (if cross-cutting).
  - Extend the relevant `Options` struct and wire `cmd.Flags().XxxVar(&opts.Field, ...)`.
  - Extend spec fill logic to map the flag onto the CR spec.

- Adding support for a new NIM resource type:
  - Add a new `ResourceType` value in `pkg/util/types.go`.
  - Extend `util.FetchResources` to list the new resource.
  - Add `get`/`status` printers.
  - Update `delete` (if deletion is supported).
  - Add `deploy` (if creation is supported).

---

## Usage examples

- Get:
  - `nim get nimservice`
  - `nim get nimservice llama3 -n nim`
  - `nim get nimcache -A`

- Status:
  - `nim status nimcache hf-cache -n models`
  - `nim status nimservice -n nim`

- Logs:
  - `nim logs collect -n nim`
  - The command prints the bundle path and enumerates saved `*.log` files.

- Delete:
  - `nim delete nimservice my-svc -n nim`
  - `nim delete nimcache my-cache`  (namespace inferred from live object)

- Deploy NIMService:
  - With existing PVC:
    - `nim deploy nimservice llama3 --image-repository=nvcr.io/nim/meta/llama-3.1-8b-instruct --tag=1.3.3 --pvc-storage-name=nim-pvc`
  - Create PVC:
    - `nim deploy nimservice llama3 --image-repository=... --tag=... --pvc-create=true --pvc-size=20Gi --pvc-volume-access-mode=ReadWriteMany --pvc-storage-class=<class>`
  - Use NIMCache storage:
    - `nim deploy nimservice llama3 --image-repository=... --tag=... --nimcache-storage-name=my-cache`

- Deploy NIMCache:
  - NGC:
    - `nim deploy nimcache ngc-cache --nim-source=ngc --model-puller=<image> --auth-secret=ngc-api-secret --profiles=fp8,h100 --gpus=h100 --precision=fp8 --engine=tensorrt_llm`
  - HF:
    - `nim deploy nimcache hf-cache --nim-source=huggingface --alt-endpoint=https://huggingface.co --alt-namespace=myorg --auth-secret=hf-secret --model-puller=<image> --pull-secret=ngc-secret --model-name=facebook/opt-1.3b`
  - NeMo DataStore:
    - `nim deploy nimcache nds-cache --nim-source=nemodatastore --alt-endpoint=https://nds.example --alt-namespace=prod --auth-secret=nds-secret --model-puller=<image> --pull-secret=ngc-secret --dataset-name=my-dataset --revision=v1`

---

## Why the Options structs are important

- They encapsulate all command inputs in one place:
  - Makes flag defaults explicit (via `pkg/util/constant.go`).
  - Supports nuanced “unset vs empty string” handling that maps to optional CR spec fields.
- They separate flag parsing from CR spec composition:
  - Dedicated `FillOut<CR>Spec` functions are easier to test and reason about.
- They keep commands cohesive and maintainable as new flags are added.

---

## RBAC/permissions note

- The CLI relies on the current kube context’s credentials.
- Users must have permission to:
  - List and get `NIMService`/`NIMCache` in targeted namespaces.
  - Create resources (for `deploy`).
  - Delete resources (for `delete`).
  - Read cluster resources (for `logs collect`, via the embedded script).

---

- Architecture: `kubectl` plugin with Cobra root `nim`, subcommands in `pkg/cmd/*`, shared utilities in `pkg/util/*`, embedded diagnostic script in `scripts/`.
- Subcommands: `get`/`status` summarize CRs; `deploy nimservice|nimcache` creates CRs; `delete` removes them; `logs collect` generates a diagnostic bundle.
- Options structs: encapsulate flags and defaults, isolate CR spec mapping, and improve testability.
- Run functions: resolve namespace/args, build typed client, fetch/create/delete resources, or execute diagnostics; output is optimized for quick human consumption.

