package akaprime

import eapaka "github.com/oyaguma3/go-eapaka"

// deriveCKPrimeIKPrime は CK'・IK' を導出する（RFC 9048 §3.3）。
// 本番では取得先が導出して返す（契約 §6.4）ので、試験で RFC 5448 の試験ベクターを通すためだけに使う。
func deriveCKPrimeIKPrime(ck, ik []byte, networkName string, autn []byte) (ckPrime, ikPrime []byte, err error) {
	return eapaka.DeriveCKPrimeIKPrime(ck, ik, networkName, autn)
}

// deriveAllKeys は CK'・IK' の導出とセッション鍵の導出をまとめて行う（試験用）。
func deriveAllKeys(identity string, ck, ik, autn []byte, networkName string) (*KeyMaterial, error) {
	ckPrime, ikPrime, err := deriveCKPrimeIKPrime(ck, ik, networkName, autn)
	if err != nil {
		return nil, err
	}
	return DeriveKeys(identity, ckPrime, ikPrime), nil
}
