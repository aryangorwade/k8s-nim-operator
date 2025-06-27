package v1alpha1

import (
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
)

var (
	reHostname = regexp.MustCompile(`^\.?[a-zA-Z0-9.-]+$`)             // .example.com or example.com
	reIPv4     = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}(:\d+)?$`)  // 10.1.2.3 or 10.1.2.3:8080
	reIPv6     = regexp.MustCompile(`^\[[0-9a-fA-F:]+\](?::\d+)?$`)    // [2001:db8::1] or [2001:db8::1]:443
	reCIDR4    = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/\d{1,2}$`) // 10.0.0.0/8
	reCIDR6    = regexp.MustCompile(`^\[[0-9a-fA-F:]+\]/\d{1,3}$`)     // [2001:db8::]/32
)

// validateNIMSourceConfiguration validates the NIMSource configuration in the NIMCache spec.
func validateNIMSourceConfiguration(source *appsv1alpha1.NIMSource, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}
	// Evalutate NGCSource if it is set. NemoDataStoreSource and HuggingFaceHubSource do not require any additional validation.
	errList = append(errList, validateNGCSource(source.NGC, fldPath.Child("ngc"))...)
	return errList
}

// ValidateNGCSource checks the NGCSource configuration.
func validateNGCSource(ngcSource *appsv1alpha1.NGCSource, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	// Return early if NGCSource is nil
	if ngcSource == nil {
		return nil
	}

	// Ensure AuthSecret is a non-empty string
	if ngcSource.AuthSecret == "" {
		errList = append(errList, field.Required(fldPath.Child("authSecret"), "must be non-empty"))
	}

	// Ensure ModelPuller is a non-empty string
	if ngcSource.ModelPuller == "" {
		errList = append(errList, field.Required(fldPath.Child("modelPuller"), "must be non-empty"))
	}

	// If Model.Profiles is not empty, ensure all other Model fields are empty
	if len(ngcSource.Model.Profiles) > 0 {
		for i, profile := range ngcSource.Model.Profiles {
			if profile == "all" && len(ngcSource.Model.Profiles) != 1 {
				errList = append(errList, field.Invalid(fldPath.Child("model").Child("profiles").Index(i), profile, "must only have a single entry when it contains 'all'"))
			}
		}

		// Ensure all other Model fields are empty
		if ngcSource.Model.Precision != "" || ngcSource.Model.Engine != "" || ngcSource.Model.TensorParallelism != "" ||
			ngcSource.Model.QoSProfile != "" || ngcSource.Model.GPUs != nil || len(ngcSource.Model.GPUs) > 0 ||
			ngcSource.Model.Lora != nil || ngcSource.Model.Buildable != nil {
			errList = append(errList, field.Forbidden(fldPath.Child("model"), "the rest of Model fields must be empty when Model.Profiles is defined"))
		}
	}

	if ngcSource.Model.QoSProfile != "" && (ngcSource.Model.QoSProfile != "latency" && ngcSource.Model.QoSProfile != "throughput") {
		errList = append(errList, field.NotSupported(fldPath.Child("model").Child("qosProfile"), ngcSource.Model.QoSProfile, []string{"latency", "throughput", ""}))
	}
	return errList
}

func validateNIMCacheStorageConfiguration(storage *appsv1alpha1.NIMCacheStorage, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}

	// Spec.Storage must not be empty
	if storage.PVC.Create == nil && storage.PVC.Name == "" && storage.PVC.StorageClass == "" &&
		storage.PVC.Size == "" && storage.PVC.VolumeAccessMode == "" && storage.PVC.SubPath == "" &&
		len(storage.PVC.Annotations) == 0 {
		errList = append(errList, field.Required(fldPath, "must not be empty"))
	}

	// If PVC.Create is False, PVC.Name cannot be empty
	if storage.PVC.Create != nil && !*storage.PVC.Create && storage.PVC.Name == "" {
		errList = append(errList, field.Required(fldPath.Child("pvc").Child("name"), "must be defined when PVC.Create is false"))
	}

	// If PVC.VolumeAccessMode is defined, it must be one of the valid modes
	validModeStrs := []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteOnce,
		corev1.ReadOnlyMany,
		corev1.ReadWriteMany,
		corev1.ReadWriteOncePod,
	}
	if storage.PVC.VolumeAccessMode != "" {
		if storage.PVC.VolumeAccessMode != "ReadWriteOnce" && storage.PVC.VolumeAccessMode != "ReadOnlyMany" &&
			storage.PVC.VolumeAccessMode != "ReadWriteMany" && storage.PVC.VolumeAccessMode != "ReadWriteOncePod" {
			errList = append(errList, field.NotSupported(fldPath.Child("pvc").Child("volumeAccessMode"), storage.PVC.VolumeAccessMode, validModeStrs))
		}
	}

	return errList
}

func validateProxyConfiguration(proxy *appsv1alpha1.ProxySpec, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}
	if proxy == nil {
		return nil
	}

	// If Proxy is not nil, ensure Proxy.NoProxy is a valid proxy string
	if proxy.NoProxy != "" {
		for i, token := range strings.Split(proxy.NoProxy, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if reHostname.MatchString(token) || reIPv4.MatchString(token) || reIPv6.MatchString(token) || reCIDR4.MatchString(token) || reCIDR6.MatchString(token) {
				continue
			}
			errList = append(errList, field.Invalid(fldPath.Child("noProxy").Index(i), token, "invalid NO_PROXY token"))
		}
	}

	// Ensure Http or Https proxy is valid
	re := regexp.MustCompile(`^https?://`)
	if proxy.HttpsProxy != "" && !re.MatchString(proxy.HttpsProxy) {
		errList = append(errList, field.Invalid(fldPath.Child("httpsProxy"), proxy.HttpsProxy, "must start with http:// or https://"))
	}
	if proxy.HttpProxy != "" && !re.MatchString(proxy.HttpProxy) {
		errList = append(errList, field.Invalid(fldPath.Child("httpProxy"), proxy.HttpProxy, "must start with http:// or https://"))
	}

	return errList
}
