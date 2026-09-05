package address

import "testing"

func TestSplitAddress(t *testing.T) {
	cases := []struct {
		in       string
		province string
		city     string
		district string
		street   string
		number   string
		detail   string
	}{
		{"静岡県静岡市清水区北矢部町  1-12-23-102 グランサラカーサ", "静岡県", "静岡市", "清水区", "北矢部町", "1-12-23-102", "グランサラカーサ"},
		{"群馬県みどり市笠懸町久宮201-118新井巖雄様方", "群馬県", "みどり市", "笠懸町", "久宮", "201-118", "新井巖雄様方"},
		{"東京都新宿区南元町20-3 ガーデンヒルズ四ツ谷迎賓の森 318室", "東京都", "新宿区", "", "南元町", "20-3", "ガーデンヒルズ四ツ谷迎賓の森 318室"},
		{"東京都調布市小島町  2ー11ー2メゾンリベルテ  201", "東京都", "調布市", "小島町", "", "2-11-2", "メゾンリベルテ 201"},
		{"大分県大分市寺崎町 2−6-24", "大分県", "大分市", "寺崎町", "", "2-6-24", ""},
		{"静岡県賀茂郡松崎町峰輪  387-14", "静岡県", "賀茂郡", "松崎町", "峰輪", "387-14", ""},
		{"千葉県松戸市五香南2-5-1 シティパル松戸元山2-507", "千葉県", "松戸市", "", "五香南", "2-5-1", "シティパル松戸元山2-507"},
		{"京都府京都市西京区御陵北山町 26-8", "京都府", "京都市", "西京区", "御陵北山町", "26-8", ""},
	}
	for _, c := range cases {
		p := splitAddress(c.in)
		if p.Province != c.province || p.City != c.city || p.District != c.district ||
			p.Street != c.street || p.Number != c.number || p.Detail != c.detail {
			t.Errorf("splitAddress(%q)\n got: %+v\nwant: province=%q city=%q district=%q street=%q number=%q detail=%q",
				c.in, p, c.province, c.city, c.district, c.street, c.number, c.detail)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{
		"２−６−２４":      "2-6-24",
		"小島町  2ー11ー2": "小島町 2-11-2",
		"グランサラカーサ":    "グランサラカーサ", // 长音符非数字间，不转换
	}
	for in, want := range cases {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
