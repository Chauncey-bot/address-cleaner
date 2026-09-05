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
		res.Validation = failValidation("GSI查询失败: " + err.Error())
		return res, err
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

// queryWithFallback 多级查询降级：精确 -> 全文 -> 去门牌 -> 到区 -> 到市
func (ac *AddressCleaner) queryWithFallback(ctx context.Context, p AddressParts) ([]GSIQueryItem, error) {
	collected := []GSIQueryItem{}
	seenItem := map[string]bool{}
	for _, q := range buildQueryVariants(p) {
		items, err := ac.queryGSI(ctx, q)
		if err != nil {
			return collected, err
		}
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
	return collected, nil
}

func buildQueryVariants(p AddressParts) []string {
	variants := []string{
		composeQuery(p, true),
		normalizeAddr(p.Raw),
		composeQuery(p, false),
		p.Province + p.City + p.District,
		p.Province + p.City,
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range variants {
		v = strings.TrimSpace(v)
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
		for _, suf := range []string{"都", "道", "府", "県", "省", "州"} {
			if idx := strings.Index(s, suf); idx >= 0 && idx <= 12 {
				p.Province = s[:idx+len(suf)]
				s = s[idx+len(suf):]
				break
			}
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
	p.Street, p.Number, p.Detail = splitStreetNumberDetail(s)
	return p
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

// splitStreetNumberDetail 拆分街道（数字前）、门牌号（数字段）、细节（数字后）
func splitStreetNumberDetail(s string) (street, number, detail string) {
	loc := numRe.FindStringIndex(s)
	if loc == nil {
		return strings.TrimSpace(s), "", ""
	}
	street = strings.TrimSpace(s[:loc[0]])
	number = s[loc[0]:loc[1]]
	detail = strings.TrimSpace(s[loc[1]:])
	return
}

// normalizeAddr 规范化：全角数字转半角、全角空格转半角、连字符统一、压缩空白
func normalizeAddr(s string) string {
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
	return strings.TrimSpace(s)
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

// numberMatchScore 门牌号匹配：全部数字组命中得20，部分命中得10
func numberMatchScore(number, title string) int {
	groups := digitGroups(number)
	if len(groups) == 0 {
		return 0
	}
	hit := 0
	for _, g := range groups {
		if strings.Contains(title, g) {
			hit++
		}
	}
	if hit == len(groups) {
		return 20
	}
	if hit > 0 {
		return 10
	}
	return 0
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
			if cjk := cjkOnly(part); len([]rune(cjk)) >= 2 && strings.Contains(nGsi, cjk) {
				norm = cjk
				tag = "匹配(CJK容错)"
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
	if p.Number != "" {
		numScore = numberMatchScore(p.Number, nGsi)
		score += numScore
	}

	// 街道为空（如“〇〇町1740-9”形式）时不阻断核心匹配，由区町村/门牌兜底
	core := cityOK && (p.Street == "" || streetOK || numScore > 0)

	threshold := 80
	if mode == "relaxed" {
		threshold = 60
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

	if opt.EnableAIAssist && ac.AIAssist != nil && v.Confidence == "low" && res.GSIAddress != "" {
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
