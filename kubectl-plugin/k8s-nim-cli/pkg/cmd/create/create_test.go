package create

import (
	"testing"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// --- NIMService tests ---

func Test_FillOutNIMServiceSpec_Valid(t *testing.T) {
	options := &NIMServiceOptions{
		ImageRepository:     "nvcr.io/nim/meta/llama-3.1-8b-instruct",
		Tag:                 "1.3.3",
		PVCCreate:           true,
		PVCStorageName:      "nim-pvc",
		PVCStorageClass:     "standard",
		PVCSize:             "20Gi",
		PVCVolumeAccessMode: string(corev1.ReadWriteMany),
		AuthSecret:          "ngc-api-secret",
		PullPolicy:          string(corev1.PullIfNotPresent),
		PullSecrets:         []string{"pullsecret"},
		ServicePort:         8080,
		ServiceType:         string(corev1.ServiceTypeClusterIP),
		Replicas:            2,
		GPULimit:            "1",
		ScaleMaxReplicas:    -1,
		ScaleMinReplicas:    -1,
		InferencePlatform:   string(appsv1alpha1.PlatformTypeStandalone),
	}

	ns, err := FillOutNIMServiceSpec(options)
	if err != nil {
		t.Fatalf("FillOutNIMServiceSpec error: %v", err)
	}

	if ns.Spec.Image.Repository != options.ImageRepository || ns.Spec.Image.Tag != options.Tag {
		t.Fatalf("image not set correctly: %+v", ns.Spec.Image)
	}
	if ns.Spec.Storage.PVC.Name != options.PVCStorageName || ns.Spec.Storage.PVC.Size != options.PVCSize {
		t.Fatalf("pvc not set correctly: %+v", ns.Spec.Storage.PVC)
	}
	if ns.Spec.Storage.PVC.VolumeAccessMode != corev1.PersistentVolumeAccessMode(options.PVCVolumeAccessMode) {
		t.Fatalf("volume access mode not set")
	}
	if ns.Spec.Expose.Service.Type != corev1.ServiceType(options.ServiceType) || ns.Spec.Expose.Service.Port == nil || *ns.Spec.Expose.Service.Port != options.ServicePort {
		t.Fatalf("service expose not set correctly: %+v", ns.Spec.Expose.Service)
	}
	if ns.Spec.Resources == nil || !ns.Spec.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")].Equal(resource.MustParse(options.GPULimit)) {
		t.Fatalf("gpu limit not set: %+v", ns.Spec.Resources)
	}
	if ns.Spec.InferencePlatform != appsv1alpha1.PlatformType(options.InferencePlatform) {
		t.Fatalf("inference platform not set")
	}
}

func Test_FillOutNIMServiceSpec_InvalidPVCMode(t *testing.T) {
	options := &NIMServiceOptions{
		ImageRepository:     "repo",
		Tag:                 "v1",
		PVCVolumeAccessMode: "InvalidMode",
		ServiceType:         string(corev1.ServiceTypeClusterIP),
		GPULimit:            "1",
	}
	_, err := FillOutNIMServiceSpec(options)
	if err == nil {
		t.Fatalf("expected error for invalid pvc-volume-access-mode")
	}
}

func Test_FillOutNIMServiceSpec_InvalidServiceType(t *testing.T) {
	options := &NIMServiceOptions{
		ImageRepository:     "repo",
		Tag:                 "v1",
		PVCVolumeAccessMode: string(corev1.ReadWriteOnce),
		ServiceType:         "BogusType",
		GPULimit:            "1",
	}
	_, err := FillOutNIMServiceSpec(options)
	if err == nil {
		t.Fatalf("expected error for invalid service-type")
	}
}

// --- NIMCache tests ---

func Test_ValidateNIMCacheOptions(t *testing.T) {
	good := &NIMCacheOptions{SourceConfiguration: "ngc"}
	if err := Validate(good); err != nil {
		t.Fatalf("Validate() good: %v", err)
	}
	bad := &NIMCacheOptions{SourceConfiguration: "something-else"}
	if err := Validate(bad); err == nil {
		t.Fatalf("Validate() expected error for invalid source")
	}
}

func Test_FillOutNIMCacheSpec_NGC(t *testing.T) {
	options := &NIMCacheOptions{
		SourceConfiguration: "ngc",
		AuthSecret:          "ngc-api-secret",
		ModelPuller:         "nvcr.io/nim/puller:latest",
		PullSecret:          "pull-secret",
		ModelEndpoint:       "https://endpoint",
		Profiles:            []string{"p1", "p2"},
		Precision:           "fp8",
		Engine:              "tensorrt_llm",
		TensorParallelism:   "2",
		QosProfile:          "throughput",
		Lora:                "true",
		Buildable:           "false",
		GPUs:                []string{"h100", "a100"},
		ResourcesCPU:        "2",
		ResourcesMemory:     "4Gi",
	}

	nc, err := FillOutNIMCacheSpec(options)
	if err != nil {
		t.Fatalf("FillOutNIMCacheSpec error: %v", err)
	}
	if nc.Spec.Source.NGC == nil {
		t.Fatalf("NGC source not set")
	}
	if nc.Spec.Source.NGC.Model == nil || nc.Spec.Source.NGC.Model.Precision != "fp8" || nc.Spec.Source.NGC.Model.Engine != "tensorrt_llm" {
		t.Fatalf("model fields not set: %+v", nc.Spec.Source.NGC.Model)
	}
	if len(nc.Spec.Source.NGC.Model.GPUs) != 2 || nc.Spec.Source.NGC.Model.GPUs[0].Product == "" {
		t.Fatalf("gpus not set: %+v", nc.Spec.Source.NGC.Model.GPUs)
	}
	if !nc.Spec.Resources.CPU.Equal(resource.MustParse("2")) || !nc.Spec.Resources.Memory.Equal(resource.MustParse("4Gi")) {
		t.Fatalf("resources not set: %+v", nc.Spec.Resources)
	}
}

func Test_FillOutNIMCacheSpec_HF(t *testing.T) {
	options := &NIMCacheOptions{
		SourceConfiguration: "huggingface",
		AltEndpoint:         "https://hf.example",
		AltNamespace:        "main",
		ModelName:           "m1",
		AuthSecret:          "hf-secret",
		ModelPuller:         "hf-puller:latest",
		PullSecret:          "hf-pull",
		Revision:            "r1",
	}
	nc, err := FillOutNIMCacheSpec(options)
	if err != nil {
		t.Fatalf("FillOutNIMCacheSpec HF error: %v", err)
	}
	if nc.Spec.Source.HF == nil || nc.Spec.Source.HF.Endpoint == "" || nc.Spec.Source.HF.Namespace == "" {
		t.Fatalf("HF fields not set: %+v", nc.Spec.Source.HF)
	}
}

func Test_FillOutNIMCacheSpec_DataStore(t *testing.T) {
	options := &NIMCacheOptions{
		SourceConfiguration: "nemodatastore",
		AltEndpoint:         "https://ds.example/v1/hf",
		AltNamespace:        "default",
		DatasetName:         "dset",
		AuthSecret:          "ds-secret",
		ModelPuller:         "ds-puller:latest",
		PullSecret:          "ds-pull",
		Revision:            "r2",
	}
	nc, err := FillOutNIMCacheSpec(options)
	if err != nil {
		t.Fatalf("FillOutNIMCacheSpec DataStore error: %v", err)
	}
	if nc.Spec.Source.DataStore == nil || nc.Spec.Source.DataStore.Endpoint == "" || nc.Spec.Source.DataStore.Namespace == "" {
		t.Fatalf("DataStore fields not set: %+v", nc.Spec.Source.DataStore)
	}
}

func Test_FillOutNIMCacheSpec_InvalidBools(t *testing.T) {
	options := &NIMCacheOptions{
		SourceConfiguration: "ngc",
		ModelPuller:         "img",
		AuthSecret:          "s",
		Lora:                "not-bool",
	}
	_, err := FillOutNIMCacheSpec(options)
	if err == nil {
		t.Fatalf("expected error for invalid --lora bool")
	}
	options.Lora = "true"
	options.Buildable = "nope"
	_, err = FillOutNIMCacheSpec(options)
	if err == nil {
		t.Fatalf("expected error for invalid --buildable bool")
	}
}
