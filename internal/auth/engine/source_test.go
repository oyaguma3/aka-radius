package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"

	"github.com/oyaguma3/aka-radius/internal/auth/eap"
	"github.com/oyaguma3/aka-radius/internal/auth/source"
	eapaka "github.com/oyaguma3/go-eapaka"
	"go.uber.org/mock/gomock"
)

// captureLogs は試験の間の slog の出力を集める。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestEngine_FetchRequest(t *testing.T) {
	// 初回の取得は、方式・ネットワーク名（anId）・トレースID を渡し、取得先は加入者から引かせる（SourceID は空）。
	// 返った取得先を EAP の会話の状態に残す。
	tests := []struct {
		name     string
		prefix   string
		eapType  uint8
		wantMeth source.Method
	}{
		{"EAP-AKA", "0", eapaka.TypeAKA, source.MethodAKA},
		{"EAP-AKA'", "6", eapaka.TypeAKAPrime, source.MethodAKAPrime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			eng, mockVector, mockCtxStore, _, _, _ := newChallengeTestEngine(ctrl)

			mockCtxStore.EXPECT().Create(gomock.Any(), testTraceID, gomock.Any()).Return(nil)
			mockVector.EXPECT().Fetch(gomock.Any(), &source.Request{
				IMSI:    testIMSI,
				Method:  tt.wantMeth,
				ANID:    testNetworkName,
				TraceID: testTraceID,
			}).Return(&source.Vector{
				RAND: testRAND, AUTN: testAUTN, XRES: testXRES, CK: testCK, IK: testIK, SourceID: testSourceID,
			}, nil)
			mockCtxStore.EXPECT().Update(gomock.Any(), testTraceID, gomock.Any()).
				DoAndReturn(func(_ context.Context, _ string, updates map[string]any) error {
					if updates["source_id"] != testSourceID {
						t.Errorf("source_id = %v, want %q", updates["source_id"], testSourceID)
					}
					return nil
				})

			result := eng.Process(context.Background(), &eap.Request{
				TraceID:    testTraceID,
				UserName:   tt.prefix + testIMSI + "@realm",
				EAPMessage: buildIdentityEAPMessage(1, tt.eapType),
			})
			if result.Action != eap.ActionChallenge {
				t.Errorf("Action = %v, want Challenge", result.Action)
			}
		})
	}
}

func TestEngine_AKAPrimeUsesReceivedKeys(t *testing.T) {
	// EAP-AKA' では、受け取った CK'・IK' から K_aut と MSK を導出する（自分で CK'・IK' を導出しない。契約 §6.4）。
	ctrl := gomock.NewController(t)
	eng, mockVector, mockCtxStore, _, _, _ := newChallengeTestEngine(ctrl)
	identity := "6" + testIMSI + "@realm"
	want := eapaka.DeriveKeysAKAPrime(identity, testCK, testIK)

	mockCtxStore.EXPECT().Create(gomock.Any(), testTraceID, gomock.Any()).Return(nil)
	mockVector.EXPECT().Fetch(gomock.Any(), gomock.Any()).Return(&source.Vector{
		RAND: testRAND, AUTN: testAUTN, XRES: testXRES, CK: testCK, IK: testIK, SourceID: testSourceID,
	}, nil)
	mockCtxStore.EXPECT().Update(gomock.Any(), testTraceID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, updates map[string]any) error {
			if updates["k_aut"] != hex.EncodeToString(want.K_aut) || updates["msk"] != hex.EncodeToString(want.MSK) {
				t.Error("K_aut / MSK are not derived from the received CK' and IK'")
			}
			return nil
		})

	eng.Process(context.Background(), &eap.Request{
		TraceID:    testTraceID,
		UserName:   identity,
		EAPMessage: buildIdentityEAPMessage(1, eapaka.TypeAKAPrime),
	})
}

func TestEngine_ResyncRequest(t *testing.T) {
	// 再同期は、直前の Challenge の取得先・RAND と端末の AUTS で取り直す（契約 §8）。
	ctrl := gomock.NewController(t)
	eng, mockVector, mockCtxStore, _, _, _ := newChallengeTestEngine(ctrl)

	keys := eapaka.DeriveKeysAKA("0"+testIMSI+"@realm", testCK, testIK)
	eapCtx := makeChallengeContext(eapaka.TypeAKA, keys.K_aut, testXRES, keys.MSK)
	eapCtx.SourceID = "other-source"
	auts := bytes.Repeat([]byte{0xA5}, 14)

	mockCtxStore.EXPECT().Get(gomock.Any(), testTraceID).Return(eapCtx, nil)
	mockVector.EXPECT().Fetch(gomock.Any(), &source.Request{
		IMSI:     testIMSI,
		SourceID: "other-source",
		Method:   source.MethodAKA,
		ANID:     testNetworkName,
		Resync:   &source.Resync{RAND: testRAND, AUTS: auts},
		TraceID:  testTraceID,
	}).Return(&source.Vector{
		RAND: testRAND, AUTN: testAUTN, XRES: testXRES, CK: testCK, IK: testIK, SourceID: "other-source",
	}, nil)
	mockCtxStore.EXPECT().Update(gomock.Any(), testTraceID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, updates map[string]any) error {
			if updates["resync_count"] != 1 {
				t.Errorf("resync_count = %v, want 1", updates["resync_count"])
			}
			return nil
		})

	result := eng.Process(context.Background(), &eap.Request{
		TraceID:    testTraceID,
		UserName:   "0" + testIMSI + "@realm",
		State:      []byte(testTraceID),
		EAPMessage: buildSyncFailureEAPMessage(2, eapaka.TypeAKA, auts),
	})
	if result.Action != eap.ActionChallenge {
		t.Errorf("Action = %v, want Challenge", result.Action)
	}
}

func TestEngine_SecondResyncRejected(t *testing.T) {
	// 2 回目の Synchronization-Failure は、取得先を呼ばずに拒否する（再同期は 1 回まで）。
	logs := captureLogs(t)
	ctrl := gomock.NewController(t)
	eng, _, mockCtxStore, _, _, _ := newChallengeTestEngine(ctrl)

	keys := eapaka.DeriveKeysAKA("0"+testIMSI+"@realm", testCK, testIK)
	eapCtx := makeChallengeContext(eapaka.TypeAKA, keys.K_aut, testXRES, keys.MSK)
	eapCtx.ResyncCount = 1

	mockCtxStore.EXPECT().Get(gomock.Any(), testTraceID).Return(eapCtx, nil)
	mockCtxStore.EXPECT().Delete(gomock.Any(), testTraceID).Return(nil)

	result := eng.Process(context.Background(), &eap.Request{
		TraceID:    testTraceID,
		UserName:   "0" + testIMSI + "@realm",
		State:      []byte(testTraceID),
		EAPMessage: buildSyncFailureEAPMessage(2, eapaka.TypeAKA, make([]byte, 14)),
	})
	if result.Action != eap.ActionReject {
		t.Errorf("Action = %v, want Reject", result.Action)
	}
	if !strings.Contains(logs.String(), `"event_id":"AUTH_RESYNC_LIMIT"`) {
		t.Errorf("log does not contain AUTH_RESYNC_LIMIT: %s", logs.String())
	}
}

func TestEngine_VectorErrorLogs(t *testing.T) {
	// 取得の誤りは分類ごとの event_id で、取得先の ID を付けて記録し、EAP-Failure で拒否する。
	tests := []struct {
		name      string
		err       error
		wantEvent string
		wantLevel string
	}{
		{"一般的な拒否", &source.Error{Kind: source.KindRejected, SourceID: testSourceID, Status: 403, Cause: "AUTHENTICATION_REJECTED"}, "VECTOR_REJECTED", "WARN"},
		{"設定の食い違い", &source.Error{Kind: source.KindRejected, SourceID: testSourceID, Status: 400, Cause: "OPTIONAL_IE_INCORRECT"}, "VECTOR_REJECTED", "ERROR"},
		{"一時的な誤り", &source.Error{Kind: source.KindTemporary, SourceID: testSourceID, Reason: "timeout"}, "VECTOR_TEMPORARY_ERR", "ERROR"},
		{"契約違反", &source.Error{Kind: source.KindContractViolation, SourceID: testSourceID, Status: 200, Reason: "avType mismatch"}, "VECTOR_CONTRACT_VIOLATION", "ERROR"},
		{"遮断器", &source.Error{Kind: source.KindCircuitOpen, SourceID: testSourceID}, "VECTOR_CIRCUIT_OPEN", "ERROR"},
		{"取得先が決まらない", &source.Error{Kind: source.KindSourceUnknown, Reason: "subscriber has no source_id"}, "VECTOR_SOURCE_UNKNOWN", "WARN"},
		{"加入者を読めない", &source.Error{Kind: source.KindLookupFailed}, "VECTOR_SOURCE_LOOKUP_ERR", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			ctrl := gomock.NewController(t)
			eng, mockVector, mockCtxStore, _, _, _ := newChallengeTestEngine(ctrl)
			mockCtxStore.EXPECT().Create(gomock.Any(), testTraceID, gomock.Any()).Return(nil)
			mockVector.EXPECT().Fetch(gomock.Any(), gomock.Any()).Return(nil, tt.err)

			result := eng.Process(context.Background(), &eap.Request{
				TraceID:    testTraceID,
				UserName:   "0" + testIMSI + "@realm",
				EAPMessage: buildIdentityEAPMessage(1, eapaka.TypeAKA),
			})
			if result.Action != eap.ActionReject || len(result.EAPMessage) == 0 {
				t.Errorf("result = %+v, want Reject with EAP-Failure", result)
			}
			out := logs.String()
			for _, want := range []string{`"event_id":"` + tt.wantEvent + `"`, `"level":"` + tt.wantLevel + `"`, `"trace_id":"` + testTraceID + `"`} {
				if !strings.Contains(out, want) {
					t.Errorf("log does not contain %s: %s", want, out)
				}
			}
		})
	}
}
