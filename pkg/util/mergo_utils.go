package util

import (
	"encoding/json"
	"reflect"

	"github.com/spf13/cast"

	"github.com/imdario/mergo"
	corev1 "k8s.io/api/core/v1"
)

// MergoDelete is a sentinel value that indicates a field is to be removed during the merging process
const MergoDelete = "MERGO_DELETE"

// AutomationConfigTransformer when we want to delete the last element of a list, if we use the
// default behaviour we will still be left with the final element from the original map. Using this
// Transformer allows us to override that behaviour and perform the merging as we expect.
type AutomationConfigTransformer struct{}

func isStringMap(elem interface{}) bool {
	return reflect.TypeOf(elem) == reflect.TypeOf(make(map[string]interface{}))
}

// withoutElementAtIndex returns the given slice without the element at the specified index
func withoutElementAtIndex(slice []interface{}, index int) []interface{} {
	return append(slice[:index], slice[index+1:]...) // slice[i+1:] returns an empty slice if i >= len(slice)
}

// mergeBoth is called when both maps have a common field
func mergeBoth(structAsMap map[string]interface{}, unmodifiedOriginalMap map[string]interface{}, key string, val interface{}) {
	switch val.(type) {
	case map[string]interface{}:
		// we already know about the key, and it's a nested map so we can continue
		merge(cast.ToStringMap(structAsMap[key]), cast.ToStringMap(unmodifiedOriginalMap[key]))
	case []interface{}:
		i, j := 0, 0
		for _, element := range cast.ToSlice(val) {
			elementsFromStruct := cast.ToSlice(structAsMap[key])

			if i >= len(elementsFromStruct) {
				break
			}

			// in the case of a nested map, we can continue the merging process
			if isStringMap(element) {
				// by marking an element as nil, we indicate that we want to delete this element
				if cast.ToSlice(structAsMap[key])[i] == nil {
					slice := cast.ToSlice(structAsMap[key])
					structAsMap[key] = withoutElementAtIndex(slice, i)
					i-- // if we removed the element at a given position, we want to examine the same index again as the contents have shifted
				} else {
					merge(cast.ToStringMap(cast.ToSlice(structAsMap[key])[i]), cast.ToStringMap(cast.ToSlice(unmodifiedOriginalMap[key])[j]))
				}
			}
			// we need to maintain 2 counters in order to prevent merging a map from "structAsMap" with a value from "unmodifiedOriginalMap"
			// that doesn't correspond to the same logical value.
			i++
			j++
		}
		// for any other type, the value has been set by the operator, so we don't want to override
		// a value from the existing Automation Config value in that case.
	}
}

// merge takes a map dst (serialized from a struct) and a map src (the map from an unmodified deployment)
// and merges them together based on a set of rules
func merge(structAsMap, unmodifiedOriginalMap map[string]interface{}) {
	for key, val := range unmodifiedOriginalMap {
		if _, ok := structAsMap[key]; !ok {
			switch val.(type) {
			case []interface{}:
				structAsMap[key] = make([]interface{}, 0)
			default:
				// if we don't know about this value, then we can just accept the value coming from the Automation Config
				structAsMap[key] = val
			}

		} else { // the value exists already in the map we have, we need to perform merge
			mergeBoth(structAsMap, unmodifiedOriginalMap, key, val)
		}
	}

	// Delete any fields marked with "util.MergoDelete"
	for key, val := range structAsMap {
		// if we're explicitly sending a value of nil, it means we want to delete the corresponding entry.
		// We don't want to ever send nil values.
		if val == MergoDelete || val == nil {
			delete(structAsMap, key)
		}
	}
}

func (t AutomationConfigTransformer) Transformer(reflect.Type) func(dst, src reflect.Value) error {
	return func(dst, src reflect.Value) error {
		dstMap := cast.ToStringMap(dst.Interface())
		srcMap := cast.ToStringMap(src.Interface())
		merge(dstMap, srcMap)
		return nil
	}
}

// MergeWith takes a structToMerge, a source map src, and returns the result of the merging, and an error
func MergeWith(structToMerge interface{}, src map[string]interface{}, transformers mergo.Transformers) (map[string]interface{}, error) {
	bytes, err := json.Marshal(structToMerge)
	if err != nil {
		return nil, err
	}
	dst := make(map[string]interface{})
	err = json.Unmarshal(bytes, &dst)
	if err != nil {
		return nil, err
	}

	if err := mergo.Merge(&dst, src, mergo.WithTransformers(transformers)); err != nil {
		return nil, err
	}

	return dst, nil
}

func createContainerMap(containers []corev1.Container) map[string]corev1.Container {
	containerMap := make(map[string]corev1.Container)
	for _, c := range containers {
		containerMap[c.Name] = c
	}
	return containerMap
}

func createMountsMap(volumeMounts []corev1.VolumeMount) map[string]corev1.VolumeMount {
	mountMap := make(map[string]corev1.VolumeMount)
	for _, m := range volumeMounts {
		mountMap[m.Name] = m
	}
	return mountMap
}

func mergeVolumeMounts(customMounts, defaultMounts []corev1.VolumeMount) ([]corev1.VolumeMount, error) {
	defaultMountsMap := createMountsMap(defaultMounts)
	customMountsMap := createMountsMap(customMounts)
	mergedVolumeMounts := []corev1.VolumeMount{}
	for _, defaultMount := range defaultMounts {
		if customMount, ok := customMountsMap[defaultMount.Name]; ok {
			// needs merge
			if err := mergo.Merge(&defaultMount, customMount, mergo.WithAppendSlice, mergo.WithOverride); err != nil {
				return nil, err
			}
		}
		mergedVolumeMounts = append(mergedVolumeMounts, defaultMount)
	}
	for _, customMount := range customMounts {
		if _, ok := defaultMountsMap[customMount.Name]; ok {
			// already merged
			continue
		}
		mergedVolumeMounts = append(mergedVolumeMounts, customMount)
	}
	return mergedVolumeMounts, nil
}

// mergeContainers merges containers identified by their name
func mergeContainers(customContainers, defaultContainers []corev1.Container) ([]corev1.Container, error) {
	defaultContainerMap := createContainerMap(defaultContainers)
	customContainerMap := createContainerMap(customContainers)
	mergedContainers := []corev1.Container{}
	for _, defaultContainer := range defaultContainers {
		if customContainer, ok := customContainerMap[defaultContainer.Name]; ok {
			// need to merge
			// merge volume mounts first
			var mergedVolumeMounts []corev1.VolumeMount
			var err error
			if mergedVolumeMounts, err = mergeVolumeMounts(customContainer.VolumeMounts, defaultContainer.VolumeMounts); err != nil {
				return nil, err
			}
			if err = mergo.Merge(&defaultContainer, customContainer, mergo.WithOverride); err != nil {
				return nil, err
			}
			defaultContainer.VolumeMounts = mergedVolumeMounts
		}
		mergedContainers = append(mergedContainers, defaultContainer)
	}
	for _, customContainer := range customContainers {
		if _, ok := defaultContainerMap[customContainer.Name]; ok {
			// custom container has been merged already
			continue
		}
		mergedContainers = append(mergedContainers, customContainer)
	}
	return mergedContainers, nil
}

// MergePodSpecs takes all of the values that exist in defaultPodTemplateSpec, and merges them into
// customPodTemplateSpec. Values that exist in both will not be touched.
func MergePodSpecs(customPodTemplateSpec corev1.PodTemplateSpec, defaultPodTemplateSpec corev1.PodTemplateSpec) (corev1.PodTemplateSpec, error) {
	var err error
	mergedContainers, err := mergeContainers(customPodTemplateSpec.Spec.Containers, defaultPodTemplateSpec.Spec.Containers)
	if err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	mergedPodTemplateSpec := corev1.PodTemplateSpec{}
	if err = mergo.Merge(&mergedPodTemplateSpec, defaultPodTemplateSpec); err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	if err = mergo.Merge(&mergedPodTemplateSpec, customPodTemplateSpec, mergo.WithOverride); err != nil {
		return corev1.PodTemplateSpec{}, err
	}
	mergedPodTemplateSpec.Spec.Containers = mergedContainers
	return mergedPodTemplateSpec, nil
}
