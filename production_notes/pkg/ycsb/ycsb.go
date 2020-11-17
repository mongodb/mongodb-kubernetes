package ycsb

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/production_notes/pkg/s3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func getPodNameForJob(ctx context.Context, c kubernetes.Clientset, jobName, namespace string) (string, error) {
	labelSelector := fmt.Sprintf("job-name=%s", jobName)

	ops := metav1.ListOptions{
		LabelSelector: labelSelector,
	}

	pod, err := c.CoreV1().Pods(namespace).List(ops)
	if err != nil {
		return "", err
	}

	if len(pod.Items) != 1 {
		return "", fmt.Errorf("more than one or zero pod found with job selector: %s", labelSelector)
	}

	return pod.Items[0].ObjectMeta.Name, nil
}

// from  returns the string in "str" from "pattern" has been found
func from(str, pattern string) string {
	pos := strings.Index(str, pattern)
	if pos == -1 {
		return ""
	}
	if pos >= len(str) {
		return ""
	}
	return str[pos:]
}

func ParseAndUploadYCSBPodLogs(ctx context.Context, c kubernetes.Clientset, namespace, jobName string) error {
	podName, err := getPodNameForJob(ctx, c, jobName, namespace)
	if err != nil {
		return err
	}

	cmd := exec.Command("kubectl", "logs", podName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", output)
	}

	results := from(string(output), "[OVERALL]")
	log.Printf("ycsb results: %s", results)

	// Upload ycsb the data to S3
	s, err := s3.NewS3Session()
	if err != nil {
		return fmt.Errorf("error while creating s3 session: %v", err)
	}

	err = s3.UploadFile(ctx, s, results, "ycsb")
	if err != nil {
		return fmt.Errorf("error while uploading to s3: %v", err)
	}

	log.Printf("successfully uploaded ycsb results to s3")
	return nil
}
