package k9o

import (
	"context"
	"testing"
)

func TestCustodyStatus(t *testing.T) {
	var seen []seenReq
	c := queryClient(t, &seen, `{"data":{"mode":"PLATFORM_MANAGED","attached":false,"provider":null,"lastJob":null},"meta":{}}`)
	st, _, kerr := c.Custody.Status(context.Background())
	if kerr != nil {
		t.Fatal(kerr)
	}
	if seen[0].method != "GET" || seen[0].path != "/@portal/custody/status" || len(seen[0].query) != 0 {
		t.Fatalf("request: %+v", seen[0])
	}
	if st.Mode != "PLATFORM_MANAGED" || st.Attached || st.Provider != nil || st.LastJob != nil || st.HSM != nil {
		t.Fatalf("status: %+v", st)
	}

	seen = nil
	c = queryClient(t, &seen, `{"data":{"mode":"HYOK","attached":true,"provider":"hsm","lastJob":null,"hsm":{"vendor":"aws-cloudhsm","clusterId":"cluster-1234567890abcdef","region":"de01","attestation":{"status":null,"verifiedAt":null},"keys":null}},"meta":{}}`)
	st, _, kerr = c.Custody.Status(context.Background())
	if kerr != nil {
		t.Fatal(kerr)
	}
	if st.Mode != "HYOK" || !st.Attached || st.Provider == nil || *st.Provider != "hsm" || st.HSM == nil {
		t.Fatalf("status: %+v", st)
	}
	h := st.HSM
	if h.Vendor == nil || *h.Vendor != "aws-cloudhsm" || h.ClusterID != "cluster-1234567890abcdef" || h.Region != "de01" ||
		h.Attestation.Status != nil || h.Attestation.VerifiedAt != nil || h.Keys != nil {
		t.Fatalf("hsm: %+v", h)
	}

	seen = nil
	c = queryClient(t, &seen, `{"data":{"mode":"BYOK","attached":true,"provider":"external_kms","lastJob":{"kind":"external_transition","status":"completed","completedAt":"2026-10-26T12:00:00.000Z"}},"meta":{}}`)
	st, _, kerr = c.Custody.Status(context.Background())
	if kerr != nil || st.LastJob == nil || st.LastJob.Kind != "external_transition" || st.LastJob.Status != "completed" || st.LastJob.CompletedAt == nil || st.HSM != nil {
		t.Fatalf("lastJob: %+v %v", st, kerr)
	}
}

func TestCustodyStatusNeedsProject(t *testing.T) {
	c, kerr := NewClient(ClientOpts{BaseURL: "https://x.api.k9o.io", APIKey: "k"})
	if kerr != nil {
		t.Skip(kerr)
	}
	if _, _, kerr := c.Custody.Status(context.Background()); kerr == nil || kerr.Code != "K9O_MISSING_PROJECT_CONTEXT" {
		t.Fatalf("got %v", kerr)
	}
}
