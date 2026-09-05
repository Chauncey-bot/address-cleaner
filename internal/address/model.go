package address

// AddressParts 地址解析结果
type AddressParts struct {
	Province string `json:"province"` // 都道府县
	City     string `json:"city"`     // 市级（市/郡/特别区）
	District string `json:"district"` // 区/町/村
	Street   string `json:"street"`   // 街道/町名
	Number   string `json:"number"`   // 门牌号
	Detail   string `json:"detail"`   // 楼栋、房间号、公寓名等
	Raw      string `json:"raw"`      // 原始输入
}

// GSIQueryItem 国土地理院查询结果条目
type GSIQueryItem struct {
	Title       string         `json:"title"`
	AddressCode string         `json:"addressCode,omitempty"`
	Longitude   float64        `json:"longitude"`
	Latitude    float64        `json:"latitude"`
	Raw         map[string]any `json:"raw,omitempty"`
}

// AddressCompareResult 原始地址与GSI地址的比对结果
type AddressCompareResult struct {
	Same            bool     `json:"same"`
	Score           int      `json:"score"`
	Reason          string   `json:"reason"`
	Items           []string `json:"items"`
	NormalizedInput string   `json:"normalizedInput"`
	NormalizedGSI   string   `json:"normalizedGSI"`
}

// JudgeSource 判定来源
type JudgeSource string

const (
	JudgeSourceRule     JudgeSource = "rule"      // 规则引擎
	JudgeSourceAIAssist JudgeSource = "ai_assist" // AI辅助复核
	JudgeSourceManual   JudgeSource = "manual"    // 人工兜底
)

// AIJudge AI复核结论
type AIJudge struct {
	Same   bool   `json:"same"`
	Score  int    `json:"score"`
	Reason string `json:"reason"`
	Model  string `json:"model"`
}

// Validation 校验结论
type Validation struct {
	IsValid       bool        `json:"isValid"`
	Same          bool        `json:"same"`
	Score         int         `json:"score"`
	Source        JudgeSource `json:"source"`
	Confidence    string      `json:"confidence"` // low | mid | high
	Reason        string      `json:"reason"`
	CompareReason string      `json:"compareReason,omitempty"`
	AIAssist      *AIJudge    `json:"aiAssist,omitempty"`
}

// CleanOptions 清洗选项
type CleanOptions struct {
	Mode           string // strict / relaxed
	EnableAIAssist bool
}

// CleanResult 清洗结果
type CleanResult struct {
	Input      string                `json:"input"`
	Parts      AddressParts          `json:"parts"`
	Candidates []GSIQueryItem        `json:"candidates"`
	Matched    *GSIQueryItem         `json:"matched,omitempty"`
	GSIAddress string                `json:"gsiAddress,omitempty"`
	Compare    *AddressCompareResult `json:"compare,omitempty"`
	Validation Validation            `json:"validation"`
}
