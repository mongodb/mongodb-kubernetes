package admissionwebhook

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	labelEnabled     = "per-pod-secret-webhook/enabled"
	annotationPrefix = "per-pod-secret-webhook/secret-name-prefix"
	volumeName       = "per-pod-cert"
	mountPath        = "/per-pod-cert"
)

// HandleMutatePods is the http.HandlerFunc for the /mutate-pods webhook endpoint.
func HandleMutatePods(w http.ResponseWriter, r *http.Request) {
	log.Printf("HandleMutatePods: received request from %s", r.RemoteAddr)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("reading body: %v", err), http.StatusBadRequest)
		return
	}

	ar := admissionv1.AdmissionReview{}
	if err := json.Unmarshal(body, &ar); err != nil {
		http.Error(w, fmt.Sprintf("decoding AdmissionReview: %v", err), http.StatusBadRequest)
		return
	}

	// Guard against malformed requests with nil Request field
	if ar.Request == nil {
		http.Error(w, "nil AdmissionRequest", http.StatusBadRequest)
		return
	}

	response := mutate(ar.Request)
	response.UID = ar.Request.UID
	ar.Response = response
	log.Printf("HandleMutatePods: pod=%s allowed=%v patch=%s", ar.Request.Name, response.Allowed, string(response.Patch))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ar); err != nil {
		// Headers already sent; log server-side only — http.Error would corrupt the response body
		log.Printf("encoding AdmissionReview response: %v", err)
	}
}

func mutate(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	pod := corev1.Pod{}
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return denyResponse(fmt.Sprintf("failed to decode pod: %v", err))
	}

	// Safe fallback: allow pods without the label (objectSelector should pre-filter, but be defensive)
	if pod.Labels[labelEnabled] != "true" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	prefix := pod.Annotations[annotationPrefix]
	if prefix == "" {
		return denyResponse(fmt.Sprintf("missing or empty annotation %q", annotationPrefix))
	}

	parts := strings.Split(pod.Name, "-")
	ordinalStr := parts[len(parts)-1]
	ordinal, err := strconv.Atoi(ordinalStr)
	if err != nil || ordinal < 0 {
		return denyResponse(fmt.Sprintf("pod name %q does not end with a valid non-negative ordinal", pod.Name))
	}

	// NOTE: this assumes the pod name ends with -<integer>, which is always true for
	// StatefulSet pods. Non-StatefulSet pods with the webhook label will be denied.
	secretName := fmt.Sprintf("%s-%d", prefix, ordinal)
	patchBytes, err := buildPatches(pod, secretName)
	if err != nil {
		return denyResponse(fmt.Sprintf("building patches: %v", err))
	}

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

func buildPatches(pod corev1.Pod, secretName string) ([]byte, error) {
	type jsonPatch struct {
		Op    string      `json:"op"`
		Path  string      `json:"path"`
		Value interface{} `json:"value"`
	}

	newVolume := map[string]interface{}{
		"name":   volumeName,
		"secret": map[string]interface{}{"secretName": secretName},
	}
	newMount := map[string]interface{}{
		"name":      volumeName,
		"mountPath": mountPath,
		"readOnly":  true,
	}

	var patches []jsonPatch

	// Add volume — initialize array if volumes is nil/empty; append otherwise.
	// Using /spec/volumes/- on a nil array produces a 422 from the API server.
	if len(pod.Spec.Volumes) == 0 {
		patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes", Value: []interface{}{newVolume}})
	} else {
		patches = append(patches, jsonPatch{Op: "add", Path: "/spec/volumes/-", Value: newVolume})
	}

	// Add volumeMount to each container (same nil-vs-append logic)
	for i, c := range pod.Spec.Containers {
		if len(c.VolumeMounts) == 0 {
			patches = append(patches, jsonPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", i),
				Value: []interface{}{newMount},
			})
		} else {
			patches = append(patches, jsonPatch{
				Op:    "add",
				Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", i),
				Value: newMount,
			})
		}
	}

	return json.Marshal(patches)
}

func denyResponse(reason string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result:  &metav1.Status{Message: reason},
	}
}
