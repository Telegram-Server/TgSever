package rpc

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	richbuilder "github.com/iamxvbaba/td/telegram/message/rich"
	"github.com/iamxvbaba/td/tg"
	"golang.org/x/net/html"
)

// 本文件是 InputRichMessageHTML / InputRichMessageMarkdown 的服务端解析器，属于
// MTProto 转换层。Bot API 网关的 HTML / Markdown 输入先转成同样的 MTProto 构造器，
// 再走这里，所以两条入口共享一套语法。
//
// HTML 在交给 richbuilder 之前先做一遍预处理：tg-time、footer 这类 richbuilder
// 不认识的标签被改写成带哨兵 scheme 的链接，解析后再还原成 TextDate / PageBlockFooter；
// 表格的 bordered/striped 与单元格对齐从原始节点里取出来补回去。

const (
	richSourceSentinelScheme = "telesrv-rich"
	richSourceDateMaxUnix    = int64(1<<31 - 1)
)

type richHTMLTableSpec struct {
	bordered bool
	striped  bool
	cells    []richHTMLTableCellSpec
}

type richHTMLTableCellSpec struct {
	align  string
	valign string
}

func parseRichHTMLSource(source string) ([]tg.PageBlockClass, error) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	tables := make([]richHTMLTableSpec, 0)
	var transform func(*html.Node) error
	transform = func(node *html.Node) error {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "img", "video", "audio", "tg-map", "tg-collage", "tg-slideshow":
				// The current local blob backend cannot materialize an arbitrary
				// rich HTML media URL atomically. Fail explicitly so Bedolaga's
				// documented one-shot no-logo retry is used instead of losing media.
				return webpageMediaEmptyErr()
			case "tg-time":
				unixTime, err := strconv.ParseInt(htmlNodeAttr(node, "unix"), 10, 64)
				if err != nil || unixTime <= 0 || unixTime > richSourceDateMaxUnix {
					return richMessageDateInvalidErr()
				}
				format := htmlNodeAttr(node, "format")
				if _, ok := richSourceDateFlags(format); !ok {
					return richMessageDateInvalidErr()
				}
				node.Data = "a"
				node.Attr = []html.Attribute{{Key: "href", Val: fmt.Sprintf("%s://time?unix=%d&format=%s", richSourceSentinelScheme, unixTime, url.QueryEscape(format))}}
			case "footer":
				node.Data = "p"
				node.Attr = nil
				anchor := &html.Node{Type: html.ElementNode, Data: "a", Attr: []html.Attribute{{Key: "href", Val: richSourceSentinelScheme + "://footer"}}}
				for child := node.FirstChild; child != nil; {
					next := child.NextSibling
					node.RemoveChild(child)
					anchor.AppendChild(child)
					child = next
				}
				node.AppendChild(anchor)
			case "table":
				tables = append(tables, richHTMLTableSpecFromNode(node))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := transform(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := transform(doc); err != nil {
		return nil, err
	}
	var normalized bytes.Buffer
	if err := html.Render(&normalized, doc); err != nil {
		return nil, richMessageInvalidErr()
	}
	blocks, err := richbuilder.ParseHTML(strings.NewReader(normalized.String()))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	postProcessRichHTML(blocks, tables)
	return blocks, nil
}

// validateParsedRichSource 校验 HTML / Markdown 解析出的 blocks。调用方已经写了
// 源文本，解析不出任何 block 就是非法输入，不能当作"没有富文本"放过；源文本不携带
// 文件（Files 为空才会走到解析），所以解析结果也不能引用任何照片或文档。
func validateParsedRichSource(blocks []tg.PageBlockClass) error {
	if len(blocks) == 0 {
		return richMessageInvalidErr()
	}
	refs, err := collectRichMessageMediaRefs(blocks)
	if err != nil {
		return err
	}
	if len(refs.photoIDs) != 0 || len(refs.documentIDs) != 0 {
		return richMessageInvalidErr()
	}
	return nil
}

func parseRichMarkdownSource(source string) ([]tg.PageBlockClass, error) {
	blocks, err := richbuilder.ParseMarkdown(strings.NewReader(source))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	return blocks, nil
}

func richHTMLTableSpecFromNode(table *html.Node) richHTMLTableSpec {
	spec := richHTMLTableSpec{bordered: htmlNodeHasAttr(table, "bordered"), striped: htmlNodeHasAttr(table, "striped")}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (child.Data == "td" || child.Data == "th") {
				spec.cells = append(spec.cells, richHTMLTableCellSpec{
					align: strings.ToLower(htmlNodeAttr(child, "align")), valign: strings.ToLower(htmlNodeAttr(child, "valign")),
				})
			}
			walk(child)
		}
	}
	walk(table)
	return spec
}

func postProcessRichHTML(blocks []tg.PageBlockClass, tables []richHTMLTableSpec) {
	tableIndex := 0
	var visit func([]tg.PageBlockClass)
	visit = func(items []tg.PageBlockClass) {
		for index, block := range items {
			switch value := block.(type) {
			case *tg.PageBlockParagraph:
				if footer, ok := richSourceFooterText(value.Text); ok {
					items[index] = &tg.PageBlockFooter{Text: postProcessRichSourceText(footer)}
				} else {
					value.Text = postProcessRichSourceText(value.Text)
				}
			case *tg.PageBlockHeading1:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockHeading2:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockHeading3:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockHeading4:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockHeading5:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockHeading6:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockFooter:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockPreformatted:
				value.Text = postProcessRichSourceText(value.Text)
			case *tg.PageBlockBlockquote:
				value.Text = postProcessRichSourceText(value.Text)
				value.Caption = postProcessRichSourceText(value.Caption)
			case *tg.PageBlockBlockquoteBlocks:
				value.Caption = postProcessRichSourceText(value.Caption)
				visit(value.Blocks)
			case *tg.PageBlockDetails:
				value.Title = postProcessRichSourceText(value.Title)
				visit(value.Blocks)
			case *tg.PageBlockTable:
				value.Title = postProcessRichSourceText(value.Title)
				if tableIndex < len(tables) {
					spec := tables[tableIndex]
					tableIndex++
					value.Bordered, value.Striped = spec.bordered, spec.striped
					cellIndex := 0
					for rowIndex := range value.Rows {
						for columnIndex := range value.Rows[rowIndex].Cells {
							cell := &value.Rows[rowIndex].Cells[columnIndex]
							cell.Text = postProcessRichSourceText(cell.Text)
							if cellIndex < len(spec.cells) {
								cellSpec := spec.cells[cellIndex]
								cell.AlignCenter = cellSpec.align == "center"
								cell.AlignRight = cellSpec.align == "right"
								cell.ValignMiddle = cellSpec.valign == "middle"
								cell.ValignBottom = cellSpec.valign == "bottom"
							}
							cellIndex++
						}
					}
				}
			}
		}
	}
	visit(blocks)
}

func postProcessRichSourceText(text tg.RichTextClass) tg.RichTextClass {
	switch value := text.(type) {
	case *tg.TextConcat:
		for i := range value.Texts {
			value.Texts[i] = postProcessRichSourceText(value.Texts[i])
		}
	case *tg.TextBold:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextItalic:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextUnderline:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextStrike:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextFixed:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextSubscript:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextSuperscript:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextMarked:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextSpoiler:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextURL:
		parsed, err := url.Parse(value.URL)
		if err == nil && parsed.Scheme == richSourceSentinelScheme && parsed.Host == "time" {
			unixTime, unixErr := strconv.ParseInt(parsed.Query().Get("unix"), 10, 32)
			flags, ok := richSourceDateFlags(parsed.Query().Get("format"))
			if unixErr == nil && ok {
				return richbuilder.Date(postProcessRichSourceText(value.Text), int(unixTime), flags)
			}
		}
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextEmail:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextPhone:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextAnchor:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextMentionName:
		value.Text = postProcessRichSourceText(value.Text)
	case *tg.TextDate:
		value.Text = postProcessRichSourceText(value.Text)
	}
	return text
}

func richSourceFooterText(text tg.RichTextClass) (tg.RichTextClass, bool) {
	link, ok := text.(*tg.TextURL)
	if !ok || link.URL != richSourceSentinelScheme+"://footer" {
		return nil, false
	}
	return link.Text, true
}

func richSourceDateFlags(format string) (richbuilder.DateFlags, bool) {
	if format == "r" || format == "R" {
		return richbuilder.DateFlags{Relative: true}, true
	}
	var flags richbuilder.DateFlags
	if format == "" {
		return flags, false
	}
	for _, value := range format {
		switch value {
		case 't':
			flags.ShortTime = true
		case 'T':
			flags.LongTime = true
		case 'd':
			flags.ShortDate = true
		case 'D':
			flags.LongDate = true
		case 'w', 'W':
			flags.DayOfWeek = true
		default:
			return richbuilder.DateFlags{}, false
		}
	}
	return flags, true
}

func htmlNodeAttr(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func htmlNodeHasAttr(node *html.Node, key string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return true
		}
	}
	return false
}
