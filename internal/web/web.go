// Package web 内嵌前端页面（清关数据 Excel 上传清洗台）
package web

import _ "embed"

// IndexHTML 清关数据上传清洗页面
//
//go:embed index.html
var IndexHTML []byte
