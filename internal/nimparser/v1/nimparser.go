/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	"github.com/NVIDIA/k8s-nim-operator/internal/nimparser"
)

const (
	// BackendTypeTensorRT indicates tensortt backend.
	BackendTypeTensorRT = "tensorrt"
)

// File represents the model files.
type File struct {
	Name string `yaml:"name" json:"name,omitempty"`
}

// Src represents model source.
type Src struct {
	RepoID string `yaml:"repo_id" json:"repo_id,omitempty"`
	Files  []File `yaml:"files" json:"files,omitempty"`
}

// Component represents source and destination for model files.
type Component struct {
	Dst string `yaml:"dst" json:"dst,omitempty"`
	Src Src    `yaml:"src" json:"src,omitempty"`
}

// Workspace represents workspace for model components.
type Workspace struct {
	Components []Component `yaml:"components" json:"components,omitempty"`
}

// NIMProfile is the model profile supported by the NIM container.
type NIMProfile struct {
	Model        string            `yaml:"model" json:"model,omitempty"`
	Release      string            `yaml:"release" json:"release,omitempty"`
	Tags         map[string]string `yaml:"tags" json:"tags,omitempty"`
	ContainerURL string            `yaml:"container_url" json:"container_url,omitempty"`
	Workspace    Workspace         `yaml:"workspace" json:"workspace,omitempty"`
}

// NIMManifest is the model manifest file.
type NIMManifest map[string]NIMProfile

// UnmarshalYAML is the custom unmarshal function for Src.
func (s *Src) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw map[string]interface{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	if repoID, ok := raw["repo_id"].(string); ok {
		s.RepoID = repoID
	}

	if files, ok := raw["files"].([]interface{}); ok {
		for _, file := range files {
			if fileStr, ok := file.(string); ok {
				s.Files = append(s.Files, File{Name: fileStr})
			} else if fileMap, ok := file.(map[interface{}]interface{}); ok {
				for k := range fileMap {
					if fileName, ok := k.(string); ok {
						s.Files = append(s.Files, File{Name: fileName})
					}
				}
			}
		}
	}

	return nil
}

// UnmarshalYAML unmarshalls given yaml data into NIM manifest struct.
func (f *File) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var name string
	if err := unmarshal(&name); err != nil {
		return err
	}
	f.Name = name
	return nil
}

func (manifest NIMManifest) MatchProfiles(modelSpec appsv1alpha1.ModelSpec, discoveredGPUs []string) ([]string, error) {
	// TODO implement me
	var selectedProfiles []string

	for hash, profile := range manifest {
		// Check precision, tensor parallelism, and QoS profile
		if (modelSpec.Precision != "" && profile.Tags["precision"] != modelSpec.Precision) ||
			(modelSpec.TensorParallelism != "" && profile.Tags["tp"] != modelSpec.TensorParallelism) ||
			(modelSpec.QoSProfile != "" && profile.Tags["profile"] != modelSpec.QoSProfile) {
			continue
		}

		// Check LoRA configuration
		if modelSpec.Lora == nil && profile.Tags["feat_lora"] == "true" {
			continue
		}
		if modelSpec.Lora != nil && profile.Tags["feat_lora"] != strconv.FormatBool(*modelSpec.Lora) {
			continue
		}

		// Determine backend type
		backend := profile.Tags["llm_engine"]
		if backend == "" {
			backend = profile.Tags["backend"]
		}

		if modelSpec.Engine != "" && !strings.Contains(backend, strings.TrimSuffix(modelSpec.Engine, "_llm")) {
			continue
		}

		// Perform GPU match only when optimized engine is selected or GPU filters are provided
		if isOptimizedEngine(modelSpec.Engine) || len(modelSpec.GPUs) > 0 {
			// Skip non optimized profiles
			if !isOptimizedEngine(backend) {
				continue
			}
			if len(modelSpec.GPUs) > 0 || len(discoveredGPUs) > 0 {
				if !matchGPUProfile(modelSpec, profile, discoveredGPUs) {
					continue
				}
			}
		}

		// Profile matched the given model parameters, add hash to the selected profiles
		selectedProfiles = append(selectedProfiles, hash)
	}

	return selectedProfiles, nil
}

func (manifest NIMManifest) GetProfilesList() []string {
	keys := slices.Collect(maps.Keys(manifest))
	return keys
}
func (manifest NIMManifest) GetProfileModel(profileID string) string {
	return manifest[profileID].Model
}
func (manifest NIMManifest) GetProfileTags(profileID string) map[string]string {
	return manifest[profileID].Tags
}
func (manifest NIMManifest) GetProfileRelease(profileID string) string {
	return manifest[profileID].Release
}

func isOptimizedEngine(engine string) bool {
	return engine != "" && strings.Contains(strings.ToLower(engine), BackendTypeTensorRT)
}

func matchGPUProfile(modelSpec appsv1alpha1.ModelSpec, profile NIMProfile, discoveredGPUs []string) bool {
	foundGPU := false

	for _, gpu := range modelSpec.GPUs {
		// Check for GPU product match
		if gpu.Product != "" {
			// Check if the product matches the "gpu" tag
			if strings.Contains(strings.ToLower(profile.Tags["gpu"]), strings.ToLower(gpu.Product)) {
				foundGPU = true
			}

			// Check if the product matches the "key" tag
			if strings.Contains(strings.ToLower(profile.Tags["key"]), strings.ToLower(gpu.Product)) {
				foundGPU = true
			}

			// If the GPU product matches, check the GPU IDs
			if foundGPU && len(gpu.IDs) > 0 {
				foundID := false
				for _, id := range gpu.IDs {
					if id == strings.TrimSuffix(profile.Tags["gpu_device"], ":10de") {
						foundID = true
						break
					}
				}

				// If the GPU product matches but no IDs match, return false
				if !foundID {
					return false
				}
			}
		}
	}

	// If a GPU product was matched and IDs (if any) also matched, return true
	if foundGPU {
		return true
	}

	// If no match was found in the specified GPUs, check the discovered GPUs
	for _, productLabel := range discoveredGPUs {
		if productLabel != "" {
			// match for llm nim format
			if strings.Contains(strings.ToLower(productLabel), strings.ToLower(profile.Tags["gpu"])) {
				return true
			}
			// match for non-llm nim format
			if matches, _ := matchesRegex(productLabel, profile.Tags["product_name_regex"]); matches {
				return true
			}
		}
	}

	// If no match found in both specified and discovered GPUs, return false
	return false
}

func matchesRegex(productLabel, regexPattern string) (bool, error) {
	// If regexPattern is empty, return false
	if regexPattern == "" {
		return false, nil
	}

	// Compile the regex pattern
	regex, err := regexp.Compile(regexPattern)
	if err != nil {
		return false, err
	}

	// Check if the productLabel matches the regex
	return regex.MatchString(productLabel), nil
}

type NIMParser struct{}

func (NIMParser) ParseModelManifest(filePath string) (nimparser.NIMManifestInterface, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var config NIMManifest
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return config, nil

}

func (NIMParser) ParseModelManifestFromRawOutput(data []byte) (nimparser.NIMManifestInterface, error) {
	var config NIMManifest
	err := yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return config, nil
}
