package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	appsv1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NIMCache tests.

func newNIMCache(name, ns string) appsv1alpha1.NIMCache {
	return appsv1alpha1.NIMCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}

func withStatus(n appsv1alpha1.NIMCache, state, pvc string, conds []metav1.Condition) appsv1alpha1.NIMCache {
	n.Status.State = state
	n.Status.PVC = pvc
	n.Status.Conditions = conds
	return n
}

func withCreatedAt(n appsv1alpha1.NIMCache, t time.Time) appsv1alpha1.NIMCache {
	n.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return n
}

func withProfiles(n appsv1alpha1.NIMCache, ps ...appsv1alpha1.NIMProfile) appsv1alpha1.NIMCache {
	n.Status.Profiles = append(n.Status.Profiles, ps...)
	return n
}

func Test_printNIMCaches(t *testing.T) {
	// First cache: Failed condition with message, has age
	c1 := newNIMCache("cache-1", "ns1")
	c1 = withStatus(c1, "NotReady", "pvc-1",
		[]metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Message: ""},
			{Type: "Failed", Status: metav1.ConditionTrue, Message: "boom", LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
		},
	)
	c1 = withCreatedAt(c1, time.Now().Add(-30*time.Minute))

	// Second cache: Ready condition, zero timestamp -> <unknown> age
	c2 := newNIMCache("cache-2", "ns2")
	c2 = withStatus(c2, "Ready", "pvc-2",
		[]metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Message: "ok"}},
	)

	list := &appsv1alpha1.NIMCacheList{Items: []appsv1alpha1.NIMCache{c1, c2}}

	var buf bytes.Buffer
	if err := printNIMCaches(list, &buf); err != nil {
		t.Fatalf("printNIMCaches error: %v", err)
	}
	out := buf.String()

	// Headers (table printer uppercases)
	for _, h := range []string{"NAME", "NAMESPACE", "STATE", "PVC", "TYPE/STATUS", "LAST TRANSITION TIME", "MESSAGE", "AGE"} {
		if !strings.Contains(out, h) {
			t.Fatalf("missing header %q in\n%s", h, out)
		}
	}

	// Row 1 assertions
	for _, s := range []string{"cache-1", "ns1", "NotReady", "pvc-1", "Failed/True", "boom"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing cell %q in:\n%s", s, out)
		}
	}

	// Row 2 assertions
	for _, s := range []string{"cache-2", "ns2", "Ready", "pvc-2", "Ready/True"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing cell %q in:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "<unknown>") {
		t.Fatalf("expected <unknown> age for zero timestamp row:\n%s", out)
	}
}

func Test_printSingleNIMCache(t *testing.T) {
	nc := newNIMCache("cache-x", "ns-x")
	nc = withStatus(nc, "Ready", "pvc-x",
		[]metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Message: "all good", LastTransitionTime: metav1.NewTime(time.Now())}},
	)
	nc = withCreatedAt(nc, time.Now().Add(-2*time.Hour))
	nc = withProfiles(nc,
		appsv1alpha1.NIMProfile{Name: "p1", Model: "m1", Release: "r1", Config: map[string]string{"k": "v"}},
		appsv1alpha1.NIMProfile{Name: "p2", Model: "m2", Release: "r2"},
	)

	var buf bytes.Buffer
	if err := printSingleNIMCache(&nc, &buf); err != nil {
		t.Fatalf("printSingleNIMCache error: %v", err)
	}
	out := buf.String()

	// Core fields
	for _, s := range []string{
		"Name: cache-x",
		"Namespace: ns-x",
		"State: Ready",
		"PVC: pvc-x",
		"Type/Status: Ready/True",
		"Message: all good",
		"Cached NIM Profiles:",
		"Name: p1, Model: m1, Release: r1, Config: map[k:v]",
		"Name: p2, Model: m2, Release: r2",
	} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %q in:\n%s", s, out)
		}
	}
}

// ---- NIMService status tests (reusing this test file as requested) ----

func newNIMService(name, ns string) appsv1alpha1.NIMService {
	return appsv1alpha1.NIMService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}

func withSvcStatus(n appsv1alpha1.NIMService, state string, available int32, conds []metav1.Condition) appsv1alpha1.NIMService {
	n.Status.State = state
	n.Status.AvailableReplicas = available
	n.Status.Conditions = conds
	return n
}

func withSvcCreatedAt(n appsv1alpha1.NIMService, t time.Time) appsv1alpha1.NIMService {
	n.ObjectMeta.CreationTimestamp = metav1.NewTime(t)
	return n
}

func Test_printNIMServices_status(t *testing.T) {
	// Item 1: Failed with message
	s1 := newNIMService("svc-1", "ns1")
	s1 = withSvcStatus(s1, "NotReady", 1, []metav1.Condition{
		{Type: "Failed", Status: metav1.ConditionTrue, Message: "oops", LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * time.Minute))},
	})
	s1 = withSvcCreatedAt(s1, time.Now().Add(-1*time.Hour))

	// Item 2: Ready, zero timestamp => <unknown>
	s2 := newNIMService("svc-2", "ns2")
	s2 = withSvcStatus(s2, "Ready", 2, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Message: "ok"},
	})

	list := &appsv1alpha1.NIMServiceList{Items: []appsv1alpha1.NIMService{s1, s2}}

	var buf bytes.Buffer
	if err := printNIMServices(list, &buf); err != nil {
		t.Fatalf("printNIMServices error: %v", err)
	}
	out := buf.String()

	// Headers
	for _, h := range []string{"NAME", "NAMESPACE", "STATE", "AVAILABLE REPLICAS", "TYPE/STATUS", "LAST TRANSITION TIME", "MESSAGE", "AGE"} {
		if !strings.Contains(out, h) {
			t.Fatalf("missing header %q in\n%s", h, out)
		}
	}

	// Row 1 assertions
	for _, s := range []string{"svc-1", "ns1", "NotReady", "1", "Failed/True", "oops"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing cell %q in:\n%s", s, out)
		}
	}

	// Row 2 assertions
	for _, s := range []string{"svc-2", "ns2", "Ready", "2", "Ready/True"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing cell %q in:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "<unknown>") {
		t.Fatalf("expected <unknown> age for zero timestamp row:\n%s", out)
	}
}
