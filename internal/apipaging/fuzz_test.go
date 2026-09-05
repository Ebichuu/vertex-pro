package apipaging

import "testing"

// FuzzDecode 是 HARD-05 的 cursor fuzz：任意字符串不得 panic；篡改或
// 随机输入必须被签名校验拒绝，只有 Encode 的输出才能往返成功。
func FuzzDecode(f *testing.F) {
	f.Add("")
	f.Add("garbage")
	f.Add("AAAA")
	f.Add(Encode(Cursor{SortKey: "created_at", ID: "job-1"}))
	f.Fuzz(func(t *testing.T, encoded string) {
		cursor, err := Decode(encoded)
		if err != nil {
			return
		}
		reencoded := Encode(cursor)
		roundTripped, err := Decode(reencoded)
		if err != nil {
			t.Fatalf("round-trip decode failed: %v", err)
		}
		if roundTripped != cursor {
			t.Fatalf("round-trip mismatch: %+v vs %+v", roundTripped, cursor)
		}
	})
}
