package address

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var numRe = regexp.MustCompile(`\d+(?:-\d+)*`)
var spaceRe = regexp.MustCompile(`\s+`)

// romajiNumRe 匹配罗马字番号，如 “5 CHOUME/CHOME”“12-BAN”“26GOU”“1 JO”。
// 长词需排在短词之前（CHOUME/BANCHI/GOU），避免短词抢先匹配。
var romajiNumRe = regexp.MustCompile(`(?i)(\d{1,4})[\s\-－−]?(CHOUME|CHOME|BANCHI|BAN|GOU|GO|JO)\b`)

// latinWordRe 匹配连续拉丁字母（用于 GSI 查询时去除罗马字，GSI 索引仅日文）
var latinWordRe = regexp.MustCompile(`[A-Za-z]+`)

// numSeqRe 匹配番号序列前缀：数字经 丁目/番地/番/号/条/地割/连字符 连接的整段，
// 如 “5丁目 12番 26号”“2丁目8番1号”“1-12-23-102”“201-118”。
// 末尾允许跟一个无后续数字的后缀（“1号”的“号”），避免残留为细节。
var numSeqRe = regexp.MustCompile(`^\s*\d[\d\s]*(?:(?:丁目|番地|地割|番|号|条|[-－−])\s*\d[\d\s]*)*(?:丁目|番地|地割|番|号|条)?`)

// kanjiNumStartRe 匹配以汉字数字开头的番号起点（“二丁目”“九八番地”“三号”“二番”）。
// “X番町”（五番町/三番町）是地名而非番号，由 kanjiNumSeqStart 排除。
// 不含“条”（“三条市”是市名）。
var kanjiNumStartRe = regexp.MustCompile(`[一二三四五六七八九十百]+(?:丁目|番地|地割|号|番)`)

// romajiSuffixMap 罗马字番号后缀 -> 日文
var romajiSuffixMap = map[string]string{
	"CHOUME": "丁目",
	"CHOME":  "丁目",
	"BANCHI": "番地",
	"BAN":    "番",
	"GOU":    "号",
	"GO":     "号",
	"JO":     "条",
}

// chomeNumRe 匹配“汉字数字 + 地址号码后缀（丁目/番/号等）”，
// 仅在号码后缀前转换汉字数字，避免误伤“三条市”“一条通”“三鷹”等地名。
var chomeNumRe = regexp.MustCompile(`([一二三四五六七八九十百]+)(丁目|番町|番|号|地割)`)

// sapporoJoRe 匹配札幌条丁目格式“北/南/東/西 + 汉字数字 + 条”（如北一条、南三条）。
// 条后缀必须带方向前缀，以区别于“三条市”“一条通”等地名。
var sapporoJoRe = regexp.MustCompile(`([南北東西])([一二三四五六七八九十百]+)条`)

// kanjiDigitMap 简单汉字数字（地址番号级别最多到百）
var kanjiDigitMap = map[rune]int{
	'一': 1, '二': 2, '三': 3, '四': 4, '五': 5,
	'六': 6, '七': 7, '八': 8, '九': 9, '十': 10, '百': 100,
}

// AddressCleaner 地址清洗器
type AddressCleaner struct {
	HTTPClient  *http.Client
	GSIEndpoint string
	AIAssist    AIJudgeClient // 可选：AI辅助复核
}

func NewCleaner() *AddressCleaner {
	return &AddressCleaner{
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
		GSIEndpoint: "https://msearch.gsi.go.jp/address-search/AddressSearch",
	}
}

// Clean 完整清洗流程：解析 -> GSI查询（多级降级） -> 择优 -> 比对 -> 校验
func (ac *AddressCleaner) Clean(ctx context.Context, input string, opt CleanOptions) (*CleanResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("address is empty")
	}
	if opt.Mode == "" {
		opt.Mode = "strict"
	}

	res := &CleanResult{Input: input}
	res.Parts = splitAddress(input)

	items, err := ac.queryWithFallback(ctx, res.Parts)
	if err != nil {
		// GSI 查询失败属于“该行无法核验”的业务结果而非服务错误：
		// 返回结构化结果（isValid=false + 失败原因），保证前端无论是否匹配都能展示。
		res.Validation = failValidation("GSI查询失败: " + err.Error())
		return res, nil
	}
	res.Candidates = items

	matched, bestScore := pickBest(res.Parts, items)
	res.Matched = matched
	if matched != nil {
		res.GSIAddress = matched.Title
		cmp := isSameAddress(res.Parts, *matched, opt.Mode)
		res.Compare = &cmp
	}
	res.Validation = ac.buildValidation(res, bestScore, opt)
	return res, nil
}

// queryWithFallback 多级查询降级：精确 -> 全文 -> 去门牌 -> 到区 -> 到市。
// 单个变体请求失败（GSI 网络抖动/超时）时不中断，继续尝试后续变体；
// 所有变体均失败才返回最后一个错误，避免一次瞬时抖动导致整行清洗失败。
func (ac *AddressCleaner) queryWithFallback(ctx context.Context, p AddressParts) ([]GSIQueryItem, error) {
	collected := []GSIQueryItem{}
	seenItem := map[string]bool{}
	var lastErr error
	for _, q := range buildQueryVariants(p) {
		items, err := ac.queryGSI(ctx, q)
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = nil
		for _, it := range items {
			if it.Title == "" || seenItem[it.Title] {
				continue
			}
			seenItem[it.Title] = true
			collected = append(collected, it)
		}
		if len(collected) > 0 {
			break
		}
	}
	if len(collected) == 0 && lastErr != nil {
		return collected, lastErr
	}
	return collected, nil
}

func buildQueryVariants(p AddressParts) []string {
	// GSI 索引仅含日文，罗马字（地名/楼名/番号）会降级召回，查询词统一去除拉丁词
	variants := []string{
		stripLatinForQuery(composeQuery(p, true)),
		stripLatinForQuery(normalizeAddr(p.Raw)),
		stripLatinForQuery(composeQuery(p, false)),
		p.Province + p.City + p.District,
		p.Province + p.City,
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range variants {
		// 异体字/别字归一后再查询（南斉院町→南斎院町），GSI对标准字形召回最准
		v = canonicalTown(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func composeQuery(p AddressParts, withNumber bool) string {
	s := p.Province + p.City + p.District + p.Street
	if withNumber && p.Number != "" {
		s += " " + p.Number
	}
	return strings.TrimSpace(s)
}

// queryGSI 调用国土地理院地址检索接口
func (ac *AddressCleaner) queryGSI(ctx context.Context, q string) ([]GSIQueryItem, error) {
	u := ac.GSIEndpoint + "?q=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ac.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gsi http %d", resp.StatusCode)
	}
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	items := make([]GSIQueryItem, 0, len(raw))
	for _, r := range raw {
		it := GSIQueryItem{Title: getString(r, "title"), Raw: r}
		if props, ok := r["properties"].(map[string]any); ok {
			it.AddressCode = getString(props, "addressCode")
			// GSI的title在properties中，顶层无title时从这里取
			if it.Title == "" {
				it.Title = getString(props, "title")
			}
		}
		if geom, ok := r["geometry"].(map[string]any); ok {
			if coords, ok := geom["coordinates"].([]any); ok && len(coords) >= 2 {
				it.Longitude = toFloat(coords[0])
				it.Latitude = toFloat(coords[1])
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// splitAddress 将地址拆分为 省/市/区/街道/门牌/细节（支持中日混合）
func splitAddress(raw string) AddressParts {
	p := AddressParts{Raw: raw}
	s := normalizeAddr(raw)

	// 1. 省/县级（都道府県省州）；“京都府”需在“都”之前特判
	if strings.HasPrefix(s, "京都府") {
		p.Province = "京都府"
		s = s[len("京都府"):]
	} else {
		// 多个后缀均命中时取位置最早者，而非列表顺序：
		// “愛知県大府市”的“府”在市名中，按列表顺序会误切为“愛知県大府”+“市”
		bestIdx, bestEnd := -1, -1
		for _, suf := range []string{"都", "道", "府", "県", "省", "州"} {
			idx := strings.Index(s, suf)
			if idx < 0 || idx > 12 {
				continue
			}
			end := idx + len(suf)
			if bestIdx == -1 || end < bestEnd {
				bestIdx, bestEnd = idx, end
			}
		}
		if bestIdx >= 0 {
			p.Province = s[:bestEnd]
			s = s[bestEnd:]
		}
	}

	// 2. 市级（市/郡），随后接区/町/村级
	if city := earliestSuffixBeforeDigit(s, "市", "郡"); city != "" {
		p.City = city
		s = strings.TrimPrefix(s, city)
		if d := earliestSuffixBeforeDigit(s, "区", "町", "村"); d != "" {
			p.District = d
			s = strings.TrimPrefix(s, d)
		}
	} else if ward := earliestSuffixBeforeDigit(s, "区"); ward != "" {
		// 东京23区等：无“市”，区即市级
		p.City = ward
		s = strings.TrimPrefix(s, ward)
	}

	// 3. 街道 / 门牌号 / 细节
	// 数据录入常把区名重复拼进街道（“緑区ほら貝緑区二丁目”），剥离残留区名避免污染查询
	if p.District != "" {
		s = spaceRe.ReplaceAllString(strings.ReplaceAll(s, p.District, " "), " ")
	}
	p.Street, p.Number, p.Detail = splitStreetNumberDetail(s)
	// 町名重复录入折叠（“緑ケ丘 緑ヶ丘”→“緑ヶ丘”），避免重复片段污染GSI查询导致召回降级
	p.Street = dedupeStreet(p.Street)
	// 跨字段重复：町名已在区/町村字段提取，街道中又残留一遍（常为别字异体，
	// 如“南斎院町”入district、“南斉院町”留在street），清空街道重复避免污染查询
	if p.District != "" && p.Street != "" && canonicalTown(p.District) == canonicalTown(p.Street) {
		p.Street = ""
	}
	return p
}

// dedupeStreet 折叠街道中重复录入的町名。录入数据常把同一町名写两遍，
// 且混用异体字/别字（“緑ケ丘 緑ヶ丘”“南斎院町南斉院町”、无空格整体重复）。
func dedupeStreet(s string) string {
	tokens := strings.Fields(s)
	if len(tokens) >= 2 {
		kept := make([]string, 0, len(tokens))
		for _, t := range tokens {
			if len(kept) > 0 && canonicalTown(kept[len(kept)-1]) == canonicalTown(t) {
				continue
			}
			kept = append(kept, t)
		}
		s = strings.Join(kept, " ")
	}
	// 无空格整体重复：“緑ヶ丘緑ヶ丘”→“緑ヶ丘”
	rs := []rune(s)
	if n := len(rs); n >= 4 && n%2 == 0 {
		if canonicalTown(string(rs[:n/2])) == canonicalTown(string(rs[n/2:])) {
			s = string(rs[:n/2])
		}
	}
	return strings.TrimSpace(s)
}

// townVariantMap 地名异体字/常见录入别字归一（映射到GSI采用的标准字形）
var townVariantMap = map[rune]rune{
	'ケ': 'ヶ', 'ヶ': 'ヶ', // 緑ケ丘＝緑ヶ丘（全角片假名/小字片假名）
	'斉': '斎', '斎': '斎', // 南斉院町＝南斎院町（斎/斉录入混用，GSI标准字形为斎）
}

// canonicalTown 地名归一：异体字/常见别字统一为标准字形，用于重复折叠、查询与比对
func canonicalTown(s string) string {
	return strings.Map(func(r rune) rune {
		if c, ok := townVariantMap[r]; ok {
			return c
		}
		return r
	}, s)
}

// earliestSuffixBeforeDigit 在第一个数字之前，寻找最早出现的后缀，返回含后缀的完整片段
func earliestSuffixBeforeDigit(s string, suffixes ...string) string {
	limit := len(s)
	if loc := numRe.FindStringIndex(s); loc != nil && loc[0] < limit {
		limit = loc[0]
	}
	region := s[:limit]
	bestIdx, bestEnd := -1, -1
	best := ""
	for _, suf := range suffixes {
		idx := strings.Index(region, suf)
		if idx < 0 {
			continue
		}
		end := idx + len(suf)
		if bestIdx == -1 || end < bestEnd || (end == bestEnd && idx < bestIdx) {
			bestIdx, bestEnd, best = idx, end, region[:end]
		}
	}
	return best
}

// splitStreetNumberDetail 拆分街道（数字前）、门牌号（番号序列）、细节（番号后的楼名/房间号）。
// 番号序列支持 “5丁目12番26号”“二丁目98番”“5-12-26”“201-118” 等形式，整体提取为 “5-12-26”；
// 序列之后的内容（如 “池”“318室”）属于细节，不计入门牌号。
func splitStreetNumberDetail(s string) (street, number, detail string) {
	loc := numberSeqStart(s)
	if loc < 0 {
		return strings.TrimSpace(s), "", ""
	}
	street = strings.TrimSpace(s[:loc])
	// 汉字番号归一（二丁目→2丁目）后再提取序列
	tail := normalizeChomeNumbers(s[loc:])
	seq := numSeqRe.FindString(tail)
	if seq == "" {
		seq = numRe.FindString(tail)
	}
	number = strings.Join(digitGroups(seq), "-")
	detail = strings.TrimSpace(tail[len(seq):])
	return
}

// numberSeqStart 返回番号序列的起始字节位置：
// 取阿拉伯数字起点与“汉字数字+丁目/番/号”起点中较早者；“X番町”为地名需排除；无番号返回 -1。
func numberSeqStart(s string) int {
	best := -1
	if loc := numRe.FindStringIndex(s); loc != nil {
		best = loc[0]
	}
	for _, loc := range kanjiNumStartRe.FindAllStringIndex(s, -1) {
		// “番”后紧跟“町”是地名（五番町/三番町），不是番号
		if strings.HasPrefix(s[loc[1]:], "町") {
			continue
		}
		if best == -1 || loc[0] < best {
			best = loc[0]
		}
	}
	return best
}

// normalizeAddr 规范化：罗马字番号转日文（5 CHOUME→5丁目）、
// 全角数字转半角、全角空格转半角、连字符统一、压缩空白
func normalizeAddr(s string) string {
	s = normalizeRomajiNumbers(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			b.WriteRune(r - '０' + '0')
		case r == '　':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	s = normalizeDashes(b.String())
	s = spaceRe.ReplaceAllString(s, " ")
	// 地名异体字/别字归一（緑ケ丘→緑ヶ丘、南斉院町→南斎院町），查询与比对口径一致
	s = canonicalTown(s)
	return strings.TrimSpace(s)
}

// normalizeRomajiNumbers 将罗马字番号转为日文后缀：
// “5 CHOUME 12 BAN 26 GOU” -> “5丁目 12番 26号”，“1 JO” -> “1条”
func normalizeRomajiNumbers(s string) string {
	return romajiNumRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := romajiNumRe.FindStringSubmatch(m)
		suffix, ok := romajiSuffixMap[strings.ToUpper(sub[2])]
		if !ok {
			return m
		}
		return sub[1] + suffix
	})
}

// stripLatinForQuery 去除拉丁字母词（罗马字地名/楼名），GSI 仅索引日文，
// 罗马字混入查询词会导致召回降级（如 “南が丘町MINAMIGAOKAMACHI 5-12-26” 只命中町名）
func stripLatinForQuery(s string) string {
	s = latinWordRe.ReplaceAllString(s, " ")
	return spaceRe.ReplaceAllString(s, " ")
}

// normalizeDashes 统一连字符：各类破折号转'-'，数字间的长音符（ー）也转'-'
func normalizeDashes(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case '－', '−', '‐', '–', '—', '―':
			b.WriteRune('-')
		case 'ー':
			prevDigit := i > 0 && unicode.IsDigit(rs[i-1])
			nextDigit := i < len(rs)-1 && unicode.IsDigit(rs[i+1])
			if prevDigit && nextDigit {
				b.WriteRune('-')
			} else {
				b.WriteRune(r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// pickBest 从候选中挑选得分最高者
func pickBest(p AddressParts, items []GSIQueryItem) (*GSIQueryItem, int) {
	best := (*GSIQueryItem)(nil)
	bestScore := -1
	for i := range items {
		s := scoreCandidate(p, items[i])
		if s > bestScore {
			bestScore = s
			best = &items[i]
		}
	}
	return best, bestScore
}

// scoreCandidate 候选打分：省份30 + 市30 + 区20 + 街道15 + 门牌20
func scoreCandidate(p AddressParts, c GSIQueryItem) int {
	title := normalizeAddr(c.Title)
	score := 0
	if p.Province != "" && strings.Contains(title, normalizeAddr(p.Province)) {
		score += 30
	}
	if p.City != "" && strings.Contains(title, normalizeAddr(p.City)) {
		score += 30
	}
	if p.District != "" && strings.Contains(title, normalizeAddr(p.District)) {
		score += 20
	}
	if p.Street != "" && strings.Contains(title, normalizeAddr(p.Street)) {
		score += 15
	}
	if p.Number != "" {
		score += numberMatchScore(p.Number, title)
	}
	return score
}

// parseKanjiNumber 解析简单汉字数字（1~999）：四→4、十→10、二十四→24、百→100
func parseKanjiNumber(s string) (int, bool) {
	total, cur := 0, 0
	valid := false
	for _, r := range s {
		v, ok := kanjiDigitMap[r]
		if !ok {
			return 0, false
		}
		valid = true
		switch r {
		case '百', '十':
			if cur == 0 {
				cur = 1
			}
			if r == '百' {
				total += cur * 100
			} else {
				total += cur * 10
			}
			cur = 0
		default:
			cur = v
		}
	}
	return total + cur, valid
}

// normalizeChomeNumbers 将“四丁目２番”“北一条”等汉字番号转为阿拉伯数字。
// 仅转换紧跟丁目/番/号等后缀的汉字数字（“四丁目”→“4丁目”），
// “条”仅限札幌格式（北/南/東/西 + 汉字 + 条），避免误伤“三条市”“一条通”“三鷹”等地名。
func normalizeChomeNumbers(s string) string {
	s = chomeNumRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := chomeNumRe.FindStringSubmatch(m)
		n, ok := parseKanjiNumber(sub[1])
		if !ok {
			return m
		}
		return strconv.Itoa(n) + sub[2]
	})
	s = sapporoJoRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := sapporoJoRe.FindStringSubmatch(m)
		n, ok := parseKanjiNumber(sub[2])
		if !ok {
			return m
		}
		return sub[1] + strconv.Itoa(n) + "条"
	})
	return s
}

// numberMatchScore 门牌号匹配（日本地址 丁目-番-号 三级）。
// GSI 数据粒度通常只到“丁目·番”（不含“号”），且番号常用汉字（四丁目、２番），
// 因此双方先做汉字番号归一，再按层级命中计分：
//   - 全部数字组命中得 20；
//   - 首组（丁目）命中且累计命中 ≥2 组（丁目+番）得 20（“号”缺失属数据粒度，不扣分）；
//   - 部分命中得 10；完全不命中得 0。
func numberMatchScore(number, title string) int {
	groups := digitGroups(normalizeChomeNumbers(number))
	if len(groups) == 0 {
		return 0
	}
	nt := normalizeChomeNumbers(title)
	hit, firstHit := 0, false
	for i, g := range groups {
		if strings.Contains(nt, g) {
			hit++
			if i == 0 {
				firstHit = true
			}
		}
	}
	switch {
	case hit == len(groups):
		return 20
	case firstHit && hit >= 2:
		return 20
	case hit > 0:
		return 10
	default:
		return 0
	}
}

// digitGroups 提取数字组："1-12-23" -> ["1","12","23"]
func digitGroups(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) })
}

// cjkOnly 仅保留中日文字符（汉字/假名），用于混合文字容错比较
func cjkOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasLatin 是否含有拉丁字母（用于判断纯罗马字地址成分）
func hasLatin(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// isSameAddress 容错比对：非严格字符串相等。
// 容错范围：全角/半角、连字符、空白、表述差异。
// 核心条件：城市命中 且（街道 或 门牌）命中，再按模式阈值判定。
func isSameAddress(p AddressParts, gsi GSIQueryItem, mode string) AddressCompareResult {
	res := AddressCompareResult{Items: []string{}}
	nIn := normalizeAddr(p.Raw)
	nGsi := normalizeAddr(gsi.Title)
	res.NormalizedInput = nIn
	res.NormalizedGSI = nGsi

	score := 0
	streetOK := false
	districtOK := false
	cityOK := false

	match := func(name, part string, weight int, cjkTolerant bool) bool {
		if part == "" {
			return false
		}
		norm := normalizeAddr(part)
		tag := "匹配"
		if !strings.Contains(nGsi, norm) && cjkTolerant {
			// 容错：混入拉丁字符/数字的街道名，退化为仅中日文字比较
			cjk := cjkOnly(part)
			if len([]rune(cjk)) >= 2 && strings.Contains(nGsi, cjk) {
				norm = cjk
				tag = "匹配(CJK容错)"
			} else if cjk == "" && hasLatin(part) {
				// 纯罗马字成分（如 MINAMIGAOKAMACHI）无法与日文标题比对，记为跳过而非不一致
				res.Items = append(res.Items, name+":罗马字跳过比对")
				return false
			}
		}
		if strings.Contains(nGsi, norm) {
			score += weight
			res.Items = append(res.Items, name+":"+tag)
			return true
		}
		res.Items = append(res.Items, name+":不一致")
		return false
	}

	match("province", p.Province, 20, false)
	cityOK = match("city", p.City, 25, false)
	districtOK = match("district", p.District, 15, false)
	streetOK = match("street", p.Street, 15, true)

	// 容错：GSI标题常省略“郡”（如“賀茂郡松崎町”→“静岡県松崎町”），
	// 若市为郡级且区町村已命中，则视为市级命中
	if !cityOK && strings.HasSuffix(p.City, "郡") && districtOK {
		cityOK = true
		score += 15
		res.Items = append(res.Items, "city:匹配(郡省略容错)")
	}

	numScore := 0
	numVerifiable := false
	if p.Number != "" {
		numScore = numberMatchScore(p.Number, nGsi)
		score += numScore
		// GSI标题含数字（番地级数据）时番号才可比对；
		// 标题无数字说明GSI只到町名粒度，番号无法核验而非不一致
		numVerifiable = numRe.MatchString(nGsi)
		if !numVerifiable {
			res.Items = append(res.Items, "number:GSI仅到町名粒度，番号无法核验")
		}
	}

	// 街道为空（如“〇〇町1740-9”形式）时不阻断核心匹配，由区町村/门牌兜底
	core := cityOK && (p.Street == "" || streetOK || numScore > 0)

	threshold := 80
	if mode == "relaxed" {
		threshold = 60
	}
	// 番号不可验证（输入无番号，或GSI仅到町名）时，番号权重不计入门槛
	if p.Number == "" || !numVerifiable {
		threshold -= 20
	}
	res.Score = score
	res.Same = core && score >= threshold
	switch {
	case res.Same:
		res.Reason = "关键成分均匹配，判定为同一地址"
	case core:
		res.Reason = "核心成分匹配，但整体得分未达阈值"
	default:
		res.Reason = "核心成分不匹配（需同时命中城市与街道/门牌）"
	}
	return res
}

// buildValidation 汇总校验结论；低置信度且开启AI时用AI复核（AI失败回退规则结论）
func (ac *AddressCleaner) buildValidation(res *CleanResult, bestScore int, opt CleanOptions) Validation {
	v := Validation{Source: JudgeSourceRule}
	threshold := 70
	if opt.Mode == "relaxed" {
		threshold = 50
	}
	if res.Compare == nil {
		v.Reason = "未从GSI获取到候选地址"
		v.Confidence = "low"
		return v
	}

	v.Same = res.Compare.Same
	v.Score = res.Compare.Score
	v.CompareReason = res.Compare.Reason
	v.Reason = res.Compare.Reason

	switch {
	case v.Score >= 80:
		v.Confidence = "high"
	case v.Score >= 60:
		v.Confidence = "mid"
	default:
		v.Confidence = "low"
	}

	v.IsValid = v.Same && bestScore >= threshold

	// 中/低置信度且开启AI时用AI复核（边界案例最容易误判）；AI失败回退规则结论
	if opt.EnableAIAssist && ac.AIAssist != nil && v.Confidence != "high" && res.GSIAddress != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if aj, err := ac.AIAssist.Judge(ctx, res.Input, res.GSIAddress); err == nil && aj != nil {
			v.AIAssist = aj
			v.Source = JudgeSourceAIAssist
			v.Score = aj.Score
			v.Same = aj.Same
			v.Reason = aj.Reason
			v.IsValid = aj.Same && aj.Score >= threshold
			if aj.Score >= 80 {
				v.Confidence = "high"
			} else if aj.Score >= 60 {
				v.Confidence = "mid"
			}
		}
	}
	return v
}

func failValidation(reason string) Validation {
	return Validation{Source: JudgeSourceRule, Confidence: "low", Reason: reason}
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
