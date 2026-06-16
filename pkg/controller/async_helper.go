//go:build v5_skip
// +build v5_skip

package controller

import (
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Job-tracking annotations on PortAllocation / NLBPool resources are stored as
// "<jobId>|<resultId>" so that we can recover both pieces of information after
// a controller restart.

func annotationKey(opKey string) string {
	return AnnotationJobPrefix + opKey
}

// getJobAnnotation reads the job annotation value for the given operation key.
func getJobAnnotation(obj client.Object, opKey string) string {
	if obj == nil {
		return ""
	}
	a := obj.GetAnnotations()
	if a == nil {
		return ""
	}
	return a[annotationKey(opKey)]
}

// setJobAnnotation writes the job annotation value for the given operation key.
func setJobAnnotation(obj client.Object, opKey, value string) {
	if obj == nil {
		return
	}
	a := obj.GetAnnotations()
	if a == nil {
		a = map[string]string{}
	}
	a[annotationKey(opKey)] = value
	obj.SetAnnotations(a)
}

// clearJobAnnotation removes the job annotation for the given operation key.
func clearJobAnnotation(obj client.Object, opKey string) {
	if obj == nil {
		return
	}
	a := obj.GetAnnotations()
	if a == nil {
		return
	}
	delete(a, annotationKey(opKey))
	obj.SetAnnotations(a)
}

// splitJobAnnotation parses the "jobId|resultId" form. If the input does not
// contain '|', the whole value is treated as the JobId and resultId is empty.
func splitJobAnnotation(value string) (jobId, resultId string) {
	parts := strings.SplitN(value, "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}
