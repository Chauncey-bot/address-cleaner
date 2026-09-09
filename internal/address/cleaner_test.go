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
		{"秋田県大仙市大曲西根字小館８５－３", "秋田県", "大仙市", "", "大曲西根字小館", "85-3", ""},
		// 罗马字番号（CHOME/BAN/GOU）混写：番号整体提取为 5-12-26；
		// MINAMIGAOKAMACHI 是已提取町名“南が丘町”的罗马字转写，折叠清空
		{"北海道紋別市南が丘町 MINAMIGAOKAMACHI 5 CHOUME 12 BAN 26 GOU", "北海道", "紋別市", "南が丘町", "", "5-12-26", ""},
		// 日文后缀番号序列同样整体提取
		{"東京都新宿区西新宿2丁目8番1号", "東京都", "新宿区", "", "西新宿", "2-8-1", ""},
		// 市名/町名的罗马字重复转写 + 逗号分隔：街道折叠清空，番号保持 3-31-17
		{"東京都立川市砂川町 Sunagawacho, Tachikawa 3-31-17", "東京都", "立川市", "砂川町", "", "3-31-17", ""},
		// “府”在市名中，省级必须取位置最早的“県”（愛知県大府市，含公司名干扰）；
		// OOBUSHI DAITOUCHOU 是“大府市大東町”的罗马字转写，折叠清空
		{"愛知県大府市大東町 OOBUSHI DAITOUCHOU 3-131 YAMATO UNYU DAIFU EIGYOUSHO（OOFU KITAZAKI）",
			"愛知県", "大府市", "大東町", "", "3-131", "YAMATO UNYU DAIFU EIGYOUSHO（OOFU KITAZAKI）"},
		// “府”在市名中（防府市），省级同样取位置最早的“県”
		{"山口県防府市防府1-2-3", "山口県", "防府市", "", "防府", "1-2-3", ""},
		// 汉字番号起点 + 重复区名剥离：“ほら貝緑区二丁目98番池” → ほら貝 / 2-98 / 池
		{"愛知県名古屋市緑区ほら貝緑区二丁目98番池", "愛知県", "名古屋市", "緑区", "ほら貝", "2-98", "池"},
		// “五番町”是地名而非番号，不得误切为门牌号
		{"東京都千代田区五番町", "東京都", "千代田区", "", "五番町", "", ""},
		// 町名重复录入 + ケ/ヶ异体：“緑ケ丘 緑ヶ丘5-4-40…” → 街道折叠并归一为“緑ヶ丘”
		{"東京都羽村市緑ケ丘 緑ヶ丘5-4-40グランルジュール205", "東京都", "羽村市", "", "緑ヶ丘", "5-4-40", "グランルジュール205"},
		// 町名重复且用别字（斎/斉）：正确字形入district，别字残留street后折叠清空
		{"愛媛県松山市南斎院町南斉院町 1038-2", "愛媛県", "松山市", "南斎院町", "", "1038-2", ""},
		// 市/区整段重复 + “丁 目”被空格拆开 + 全角数字：番号应为 3-9-6
		{"大阪府大阪市東住吉区南田辺大阪市 東住吉区 ３丁 目 ９－６", "大阪府", "大阪市", "東住吉区", "南田辺", "3-9-6", ""},
		// 市名简写重复（“北九州”无“市”字）+ 区名重复
		{"福岡県北九州市八幡西区則松 北九州 八幡西区5丁目13-17 於久アパート202号", "福岡県", "北九州市", "八幡西区", "則松", "5-13-17", "於久アパート202号"},
		// 市名自身含“市”字（市原市/四日市市）：市级边界取闭合的最后一个“市”
		{"千葉県市原市高滝 886-184 816", "千葉県", "市原市", "", "高滝", "886-184-816", ""},
		{"三重県四日市市諏訪町1-5", "三重県", "四日市市", "諏訪町", "", "1-5", ""},
		// 市名叠字（町名缺大字前缀且多“市”）：叠字规则产出 宇治市市，由查询/比对层折叠容错
		{"京都府宇治市市妙楽76-15", "京都府", "宇治市市", "", "妙楽", "76-15", ""},
	}
	for _, c := range cases {
		p := splitAddress(c.in)
		if p.Province != c.province || p.City != c.city || p.District != c.district || p.Street != c.street || p.Number != c.number || p.Detail != c.detail {
			t.Errorf("splitAddress(%q)\n got: %+v\nwant: province=%q city=%q district=%q street=%q number=%q detail=%q",
				c.in, p, c.province, c.city, c.district, c.street, c.number, c.detail)
		}
	}
}

func TestSplitAddressBanchi(t *testing.T) {
	cases := map[string]string{
		"秋田県大仙市大曲西根字小館８５－３":                        "85",
		"東京都新宿区西新宿2丁目8番1号":                         "8",
		"北海道紋別市南が丘町5丁目12番26号":                      "12",
		"東京都立川市砂川町 Sunagawacho, Tachikawa 3-31-17": "31",
	}
	for input, want := range cases {
		if got := splitAddress(input).Banchi; got != want {
			t.Errorf("splitAddress(%q).Banchi = %q, want %q", input, got, want)
		}
	}
}

func TestExtractBanchi(t *testing.T) {
	cases := map[string]string{
		"85-3":    "85",
		"5-12-26": "12",
		"3-9-6":   "9",
		"201-118": "201",
		"":        "",
	}
	for number, want := range cases {
		if got := extractBanchi(number); got != want {
			t.Errorf("extractBanchi(%q) = %q, want %q", number, got, want)
		}
	}
}

func TestCollapseDoubleShi(t *testing.T) {
	cases := map[string]string{
		"京都府宇治市市妙楽 76-15": "京都府宇治市妙楽 76-15",
		"宇治市市":            "宇治市",
		"千葉県市原市":          "千葉県市原市", // 无叠字不变
		"三重県四日市市諏訪町":      "三重県四日市諏訪町",
		"":                "",
	}
	for in, want := range cases {
		if got := collapseDoubleShi(in); got != want {
			t.Errorf("collapseDoubleShi(%q) = %q, want %q", in, got, want)
		}
	}
}

// GSI 仅返回市级（町名召不回）时的粒度容错：城市折叠命中 + 町名无法核验不阻断
func TestIsSameAddressGranularityTolerance(t *testing.T) {
	p := splitAddress("京都府宇治市市妙楽76-15")
	gsi := GSIQueryItem{Title: "京都府宇治市"}
	res := isSameAddress(p, gsi, []GSIQueryItem{gsi}, "strict")
	if !res.Same {
		t.Fatalf("isSameAddress(宇治市市妙楽) Same = false, items=%v", res.Items)
	}
	hasStreetTolerance, hasCityTolerance := false, false
	for _, it := range res.Items {
		if it == "street:GSI未含町名，无法核验" {
			hasStreetTolerance = true
		}
		if it == "city:匹配(市重字折叠容错)" {
			hasCityTolerance = true
		}
	}
	if !hasStreetTolerance || !hasCityTolerance {
		t.Errorf("items missing tolerance tags: %v", res.Items)
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{
		"２−６−２４":         "2-6-24",
		"小島町  2ー11ー2":    "小島町 2-11-2",
		"グランサラカーサ":       "グランサラカーサ",      // 长音符非数字间，不转换
		"砂川町, Tachikawa": "砂川町 Tachikawa", // 逗号转空格，避免污染查询词
	}
	for in, want := range cases {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRomajiNumbers(t *testing.T) {
	cases := map[string]string{
		"5 CHOUME 12 BAN 26 GOU":   "5丁目 12番 26号",
		"5chome 12ban 26gou":       "5丁目 12番 26号",
		"MINAMIGAOKAMACHI 5-12-26": "MINAMIGAOKAMACHI 5-12-26", // 连字符番号不变
		"北1条西2丁目":                  "北1条西2丁目",                  // 日文不受影响
		"3 BANCHI":                 "3番地",
		"1 JO":                     "1条",
		"26GO":                     "26号",
	}
	for in, want := range cases {
		if got := normalizeRomajiNumbers(in); got != want {
			t.Errorf("normalizeRomajiNumbers(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripLatinForQuery(t *testing.T) {
	cases := map[string]string{
		"北海道紋別市南が丘町MINAMIGAOKAMACHI 5-12-26": "北海道紋別市南が丘町 5-12-26",
		"東京都港区芝公園 ABCビル 4-2-8":               "東京都港区芝公園 ビル 4-2-8",
	}
	for in, want := range cases {
		if got := stripLatinForQuery(in); got != want {
			t.Errorf("stripLatinForQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseKanjiNumber(t *testing.T) {
	cases := map[string]int{
		"一": 1, "四": 4, "十": 10, "十二": 12, "二十": 20, "二十四": 24,
		"百": 100, "二百": 200, "三百五": 305,
	}
	for in, want := range cases {
		got, ok := parseKanjiNumber(in)
		if !ok || got != want {
			t.Errorf("parseKanjiNumber(%q) = %d,%v want %d,true", in, got, ok, want)
		}
	}
	if _, ok := parseKanjiNumber("三鷹"); ok {
		t.Errorf("parseKanjiNumber(%q) should fail", "三鷹")
	}
}

func TestNormalizeChomeNumbers(t *testing.T) {
	cases := map[string]string{
		"東京都港区芝公園四丁目2番":  "東京都港区芝公園4丁目2番", // 汉字丁目前数字转换
		"東京都新宿区西新宿二丁目8番": "東京都新宿区西新宿2丁目8番",
		"梅田一丁目1番3号":      "梅田1丁目1番3号",    // 丁目汉字转、番号已是半角不变
		"北海道札幌市中央区北一条":   "北海道札幌市中央区北1条", // 条后缀
		"東京都千代田区三番町":     "東京都千代田区3番町",   // 番町（番号地名，语义一致）
		"新潟県三条市":         "新潟県三条市",       // “三条市”是市名，不得转换
		"京都府京都市一条通":      "京都府京都市一条通",    // “一条通”无号码后缀，不得转换
		"東京都三鷹市":         "東京都三鷹市",       // 普通地名含汉字数字，不得转换
	}
	for in, want := range cases {
		if got := normalizeChomeNumbers(in); got != want {
			t.Errorf("normalizeChomeNumbers(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNumberMatchScore(t *testing.T) {
	cases := []struct {
		number string
		title  string
		want   int
		note   string
	}{
		{"4-2-8", "東京都港区芝公園4丁目2番", 20, "丁目+番命中、号缺失（GSI粒度）应满分"},
		{"2-8-1", "東京都新宿区西新宿2丁目8番", 20, "汉字丁目番归一后命中"},
		{"1-1-3", "大阪府大阪市北区梅田1丁目1番3号", 20, "三级全命中"},
		{"1", "北海道札幌市中央区北1条", 20, "单组命中"},
		{"1-2-3", "沖縄県那覇市松尾5番", 0, "数字组完全不命中"},
		{"5-6", "東京都千代田区丸の内5丁目", 10, "仅部分命中"},
	}
	for _, c := range cases {
		// title 模拟 GSI 返回（可能含汉字番号），numberMatchScore 内部负责归一
		if got := numberMatchScore(c.number, c.title); got != c.want {
			t.Errorf("numberMatchScore(%q, %q) = %d, want %d（%s）", c.number, c.title, got, c.want, c.note)
		}
	}
}
