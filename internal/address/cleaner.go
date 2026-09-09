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

// spacedChomeRe 匹配被空白拆开的“丁 目”（录入时常误加空格，如“３丁 目”）
var spacedChomeRe = regexp.MustCompile(`丁\s+目`)

// punctRe 匹配逗号类标点（中英文逗号/顿号）。混合录入地址常用逗号分隔
// 罗马字重复转写（“砂川町 Sunagawacho, Tachikawa”），残留进查询词会降级
// GSI 召回（町级），统一转为空格。
var punctRe = regexp.MustCompile("[,，、]")

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
		// 市名叠字折叠：解析为保「市原市/四日市市」正确性会产出 City=宇治市市，
		// 若折叠形态命中 GSI 标题，则以官方市名参与比对与输出
		if c := collapseDoubleShi(res.Parts.City); c != res.Parts.City && strings.Contains(normalizeAddr(matched.Title), normalizeAddr(c)) {
			res.Parts.City = c
		}
		cmp := isSameAddress(res.Parts, *matched, items, opt.Mode)
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
		// 市名叠字容错：「宇治市市妙楽」折叠为「宇治市妙楽」再查
		stripLatinForQuery(collapseDoubleShi(composeQuery(p, true))),
		stripLatinForQuery(normalizeAddr(p.Raw)),
		stripLatinForQuery(composeQuery(p, false)),
		stripLatinForQuery(collapseDoubleShi(composeQuery(p, false))),
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

	// 2. 市级（市/郡），随后接区/町/村级。
	// 市名自身可含“市”（市原市/四日市市/市川市），取第一个核心非空且非叠字的市/郡边界。
	if city := cityBoundaryBeforeDigit(s, "市", "郡"); city != "" {
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
	// 数据录入常把市/区名重复拼进街道（“南田辺大阪市 東住吉区 3丁目”、
	// “則松 北九州 八幡西区”“ほら貝緑区二丁目”），剥离残留行政区名避免污染GSI查询。
	// 同时处理简写重复（北九州市→“北九州”、八幡西区→“八幡西”），简写基底≥3字以防误伤。
	for _, admin := range []string{p.City, p.District} {
		for _, name := range adminResidueNames(admin) {
			s = spaceRe.ReplaceAllString(strings.ReplaceAll(s, name, " "), " ")
		}
	}
	p.Street, p.Number, p.Detail = splitStreetNumberDetail(s)
	p.Banchi = extractBanchi(p.Number)
	// 町名重复录入折叠（“緑ケ丘 緑ヶ丘”→“緑ヶ丘”），避免重复片段污染GSI查询导致召回降级
	p.Street = dedupeStreet(p.Street)
	// 罗马字重复转写折叠：“砂川町 Sunagawacho, Tachikawa”中街道仅剩罗马字词，
	// 是已提取日文町名/市名的转写，剔除避免污染输出与比对
	p.Street = foldRomajiStreet(p.Street, p.District)
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

// foldRomajiStreet 折叠街道中纯拉丁词（罗马字重复转写）。日文町名已入 district 时，
// 街道里的纯拉丁词（“Sunagawacho”“Tachikawa”）是同一地名的罗马字转写：
// GSI 查询本就剔除拉丁词、比对也跳过纯罗马字成分，保留只会污染输出，故剔除。
// 混合词（如“ABCビル”）含日文信息，保留；无日文町名提取时（district为空）不动，
// 避免“MINAMIGAOKAMACHI 5-12-26”这类纯罗马字町名输入丢失唯一地名信息。
func foldRomajiStreet(street, district string) string {
	if district == "" || street == "" || !hasLatin(street) {
		return street
	}
	tokens := strings.Fields(street)
	out := make([]string, 0, len(tokens))
	dropped := false
	for _, t := range tokens {
		if isLatinWord(t) {
			dropped = true
			continue
		}
		out = append(out, t)
	}
	if !dropped {
		return street
	}
	return strings.Join(out, " ")
}

// isLatinWord 词元是否为纯拉丁字母词（罗马字转写词元）
func isLatinWord(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// townVariantMap 地名异体字/常见录入别字归一（映射到GSI采用的标准字形）
var townVariantMap = map[rune]rune{
	'ケ': 'ヶ', 'ヶ': 'ヶ', // 緑ケ丘＝緑ヶ丘（全角片假名/小字片假名）
	'斉': '斎', '斎': '斎', // 南斉院町＝南斎院町（斎/斉录入混用，GSI标准字形为斎）
}

// adminResidueNames 返回需从街道中剥离的行政区名形态：
// 完整名（北九州市）+ 去掉行政后缀的简写（北九州）。
// 简写基底需≥3个字符（避免“津市→津”“緑区→緑”等短形误伤地名）。
func adminResidueNames(admin string) []string {
	admin = strings.TrimSpace(admin)
	if admin == "" {
		return nil
	}
	names := []string{admin}
	for _, suf := range []string{"市", "区", "郡", "町", "村"} {
		if strings.HasSuffix(admin, suf) {
			base := strings.TrimSuffix(admin, suf)
			if len([]rune(base)) >= 3 {
				names = append(names, base)
			}
			break
		}
	}
	return names
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

// cityBoundaryBeforeDigit 在第一个数字之前，定位市级（市/郡）边界并返回含后缀的完整片段。
// 规则：取第一个“核心非空（后缀前至少1字）、且后面不紧跟同一后缀字”的后缀位置。
// 这样既处理市名自身含“市”的市原市/四日市市/市川市（“四日市+市”中的第一个市后紧跟市，
// 属于市名组成字需跳过），又不会把街道里重复出现的市名（“…南田辺大阪市…”）吸进市级。
func cityBoundaryBeforeDigit(s string, suffixes ...string) string {
	limit := len(s)
	if loc := numRe.FindStringIndex(s); loc != nil && loc[0] < limit {
		limit = loc[0]
	}
	region := s[:limit]
	bestEnd := -1
	for _, suf := range suffixes {
		searchStart := 0
		for {
			rel := strings.Index(region[searchStart:], suf)
			if rel < 0 {
				break
			}
			idx := searchStart + rel
			after := idx + len(suf)
			// 核心非空，且不是“市市/郡郡”叠字（叠字时前一个后缀是市名组成字）
			if idx >= 1 && (after >= len(region) || !strings.HasPrefix(region[after:], suf)) {
				if bestEnd == -1 || after < bestEnd {
					bestEnd = after
				}
				break // 该后缀取第一个合格位置
			}
			searchStart = after
		}
	}
	if bestEnd < 0 {
		return ""
	}
	return region[:bestEnd]
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

// extractBanchi 提取番号中的番名（番/番地对应的数字组）。
// 三组番号按“丁目-番-号”取第二组；两组或一组番号按“番-号”或“番地”取第一组。
func extractBanchi(number string) string {
	groups := digitGroups(number)
	if len(groups) == 0 {
		return ""
	}
	if len(groups) >= 3 {
		return groups[1]
	}
	return groups[0]
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
	// 逗号类标点转空格（“Sunagawacho, Tachikawa”），避免残留进GSI查询词
	s = punctRe.ReplaceAllString(s, " ")
	s = spaceRe.ReplaceAllString(s, " ")
	// “丁 目”被录入空格拆开时合并（“３丁 目”→“３丁目”），否则番号序列无法识别
	s = spacedChomeRe.ReplaceAllString(s, "丁目")
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

// collapseDoubleShi 折叠重复的“市”字：「宇治市市妙楽」→「宇治市妙楽」。
// 市名叠字规则（服务 市原市/四日市市）会把「宇治市市妙楽」解析出 City=宇治市市，
// 而官方市名无叠字，查询与比对时用折叠形态容错。
func collapseDoubleShi(s string) string {
	return strings.ReplaceAll(s, "市市", "市")
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
	} else if c := collapseDoubleShi(normalizeAddr(p.City)); c != "" && c != normalizeAddr(p.City) && strings.Contains(title, c) {
		// 市名叠字折叠容错（City=宇治市市 → 宇治市）
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
// candidates 为全部 GSI 候选，用于判断町名是否可核验（GSI粒度容错）。
func isSameAddress(p AddressParts, gsi GSIQueryItem, candidates []GSIQueryItem, mode string) AddressCompareResult {
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
	if !cityOK {
		// 市名叠字折叠容错：City=宇治市市 折叠为 宇治市 后可命中官方标题
		if c := collapseDoubleShi(normalizeAddr(p.City)); c != "" && c != normalizeAddr(p.City) && strings.Contains(nGsi, c) {
			cityOK = true
			score += 25
			res.Items = append(res.Items, "city:匹配(市重字折叠容错)")
		}
	}
	districtOK = match("district", p.District, 15, false)
	// 町名可核验性：任一候选含町名才可比对；GSI 仅返回市级（町名缺大字前缀等
	// 导致召不回町级）时，町名属于无法核验而非不一致
	streetVerifiable := p.Street == "" || streetVerifiableIn(p, candidates)
	if p.Street == "" || streetVerifiable {
		streetOK = match("street", p.Street, 15, true)
	} else {
		res.Items = append(res.Items, "street:GSI未含町名，无法核验")
	}

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

	// 街道为空（如“〇〇町1740-9”形式）时不阻断核心匹配，由区町村/门牌兜底；
	// 町名无法核验（GSI仅到市级）时同样不阻断，由市级+阈值降权兜底
	core := cityOK && (p.Street == "" || streetOK || !streetVerifiable || numScore > 0)

	threshold := 80
	if mode == "relaxed" {
		threshold = 60
	}
	// 番号不可验证（输入无番号，或GSI标题无数字）时，番号权重不计入门槛
	if p.Number == "" || !numVerifiable {
		threshold -= 20
	}
	// 町名不可核验（GSI仅到市级）时，街道权重不计入门槛
	if !streetVerifiable {
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

// streetVerifiableIn 町名是否可核验：任一候选标题（归一后）包含町名。
// GSI 仅返回市级（町名召不回，如「宇治妙楽」被写成「市妙楽」缺大字前缀）时
// 町名无法核验，比对按数据粒度容错处理，而非判为町名不一致。
func streetVerifiableIn(p AddressParts, items []GSIQueryItem) bool {
	ns := normalizeAddr(p.Street)
	if ns == "" {
		return true
	}
	for i := range items {
		if strings.Contains(normalizeAddr(items[i].Title), ns) {
			return true
		}
	}
	return false
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

	// GSI 数据粒度不足（町名/番号无法核验）时同步下调判定门槛，与比对层口径一致
	if res.Parts.Street != "" && !streetVerifiableIn(res.Parts, res.Candidates) {
		threshold -= 20
	}
	if res.Parts.Number == "" || !numRe.MatchString(res.Compare.NormalizedGSI) {
		threshold -= 20
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
