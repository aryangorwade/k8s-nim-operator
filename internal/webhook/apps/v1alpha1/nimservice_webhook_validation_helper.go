package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
)

func validateImageConfiguration(image *appsv1alpha1.Image, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}
	if image.Repository == "" {
		errList = append(errList, field.Required(fldPath.Child("repository"), "is required"))
	}
	if image.Tag == "" {
		errList = append(errList, field.Required(fldPath.Child("tag"), "is required"))
	}
	return errList
}

func validateServiceStorageConfiguration(nimService *appsv1alpha1.NIMService, fldPath *field.Path) field.ErrorList {
	errList := field.ErrorList{}
	storage := nimService.Spec.Storage
	// If size limit is defined, it must be greater than 0
	if storage.SharedMemorySizeLimit != nil {
		if storage.SharedMemorySizeLimit.Sign() <= 0 {
			errList = append(errList, field.Invalid(fldPath.Child("sharedMemorySizeLimit"), storage.SharedMemorySizeLimit.String(), "must be > 0"))
		}
	}

	// Check if nimCache is defined (non-empty)
	nimCacheDefined := storage.NIMCache.Name != "" || storage.NIMCache.Profile != ""

	// Check if PVC is defined (non-empty)
	pvcDefined := storage.PVC.Create != nil || storage.PVC.Name != "" || storage.PVC.StorageClass != "" ||
		storage.PVC.Size != "" || storage.PVC.VolumeAccessMode != "" || storage.PVC.SubPath != "" ||
		len(storage.PVC.Annotations) > 0

	// Ensure exactly one of nimCache or PVC is defined
	if !nimCacheDefined && !pvcDefined {
		errList = append(errList, field.Required(fldPath, "exactly one of nimCache or pvc must be defined"))
	} else if nimCacheDefined && pvcDefined {
		errList = append(errList, field.Invalid(fldPath, "both nimCache and pvc defined", "exactly one of nimCache or pvc must be defined, not both"))
	}

	// If NIMCache is non-nil, NIMCache.Name must not be empty
	if storage.NIMCache.Profile != "" {
		if storage.NIMCache.Name == "" {
			// return fmt.Errorf("NIMService.Spec.Storage.NIMCache.Name is required when NIMService.Spec.Storage.NIMCache is defined")
			errList = append(errList, field.Required(fldPath.Child("nimCache").Child("name"), "is required when NIMCache is defined"))
		}
	}

	// Enforcing PVC rules if defined
	if pvcDefined {

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
	}
	return errList
}

func validateDRAResourcesConfiguration(nimService *appsv1alpha1.NIMService, fldPath *field.Path) field.ErrorList {
	// Rules for only DRAResource.ResourceClaimTemplateName being defined
	errList := field.ErrorList{}
	if nimService.Spec.Replicas > 1 || (nimService.Spec.Scale.Enabled != nil && *nimService.Spec.Scale.Enabled) {
		// Only DRAResource.ResourceClaimTemplateName must be defined (not ResourceClaimName)
		for i, dra := range nimService.Spec.DRAResources {
			if dra.ResourceClaimTemplateName == nil || *dra.ResourceClaimTemplateName == "" {
				errList = append(errList, field.Required(fldPath.Index(i).Child("resourceClaimTemplateName"), "must be defined when using multiple replicas or autoscaling"))
			}
			if dra.ResourceClaimName != nil && *dra.ResourceClaimName != "" {
				errList = append(errList, field.Forbidden(fldPath.Index(i).Child("resourceClaimName"), "must not be set when using multiple replicas or autoscaling; only ResourceClaimTemplateName is allowed"))
			}
		}
	} else {

		// If DRAResources is not empty, all DRAResources objects must have a unique DRAResource.ResourceClaimName
		draResources := nimService.Spec.DRAResources
		if len(draResources) > 0 {
			seen := make(map[string]struct{})
			for i, dra := range draResources {
				if dra.ResourceClaimName == nil {
					errList = append(errList, field.Required(fldPath.Index(i).Child("resourceClaimName"), "must not be empty"))
					continue
				}
				if _, exists := seen[*dra.ResourceClaimName]; exists {
					errList = append(errList, field.Duplicate(fldPath.Index(i).Child("resourceClaimName"), *dra.ResourceClaimName))
				}
				seen[*dra.ResourceClaimName] = struct{}{}
			}
		}
	}
	return errList
}
