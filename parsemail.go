package parsemail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

const contentTypeMultipartSigned = "multipart/signed"
const contentTypeMultipartMixed = "multipart/mixed"
const contentTypeMultipartAlternative = "multipart/alternative"
const contentTypeMultipartRelated = "multipart/related"
const contentTypeTextCalendar = "text/calendar"
const contentTypeTextHtml = "text/html"
const contentTypeTextPlain = "text/plain"
const contentTypeTextExtension = "text/x-"
const contentTypeApplicationOctetStream = "application/octet-stream"
const maxDepthOfMultipartMixed = 3

// Parse an email message read from io.Reader into parsemail.Email struct
func Parse(r io.Reader) (email Email, err error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return
	}

	email, err = createEmailFromHeader(msg.Header)
	if err != nil {
		return
	}

	email.ContentType = msg.Header.Get("Content-Type")
	contentType, params, err := parseContentType(email.ContentType)
	if err != nil {
		return
	}

	switch contentType {
	case contentTypeMultipartSigned:
		email.TextBody, email.HTMLBody, email.Attachments, email.EmbeddedFiles, email.TextBodies, email.HTMLBodies, err = parseMultipartMixed(msg.Body, params["boundary"], 1)
	case contentTypeMultipartMixed:
		email.TextBody, email.HTMLBody, email.Attachments, email.EmbeddedFiles, email.TextBodies, email.HTMLBodies, err = parseMultipartMixed(msg.Body, params["boundary"], 1)
	case contentTypeMultipartAlternative:
		email.TextBody, email.HTMLBody, email.Attachments, email.EmbeddedFiles, email.TextBodies, email.HTMLBodies, err = parseMultipartAlternative(msg.Body, params["boundary"])
	case contentTypeMultipartRelated:
		email.TextBody, email.HTMLBody, email.Attachments, email.EmbeddedFiles, email.TextBodies, email.HTMLBodies, err = parseMultipartRelated(msg.Body, params["boundary"])
	case contentTypeTextPlain:
		buf := new(bytes.Buffer)
		tee := io.TeeReader(msg.Body, buf)
		message, _ := ioutil.ReadAll(tee)
		email.TextBody = strings.TrimSuffix(string(message[:]), "\n")
		var data io.Reader
		data, err = decodeContent(buf, email.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			return
		}
		email.TextBodies = []*TextBody{
			{
				Body{
					ContentType: contentType,
					Params:      params,
					Data:        data,
				},
			},
		}
	case contentTypeTextHtml:
		buf := new(bytes.Buffer)
		tee := io.TeeReader(msg.Body, buf)
		message, _ := ioutil.ReadAll(tee)
		email.HTMLBody = strings.TrimSuffix(string(message[:]), "\n")
		var data io.Reader
		data, err = decodeContent(buf, email.Header.Get("Content-Transfer-Encoding"))
		if err != nil {
			return
		}
		email.HTMLBodies = []*HTMLBody{
			{
				Body{
					ContentType: contentType,
					Params:      params,
					Data:        data,
				},
			},
		}
	default:
		email.Content, err = decodeContent(msg.Body, msg.Header.Get("Content-Transfer-Encoding"))
	}

	return
}

func createEmailFromHeader(header mail.Header) (email Email, err error) {
	hp := headerParser{header: &header}

	email.Subject = decodeMimeSentence(header.Get("Subject"))
	email.From = hp.parseAddressList(header.Get("From"))
	email.Sender = hp.parseAddress(header.Get("Sender"))
	email.ReplyTo = hp.parseAddressList(header.Get("Reply-To"))
	email.To = hp.parseAddressList(header.Get("To"))
	email.Cc = hp.parseAddressList(header.Get("Cc"))
	email.Bcc = hp.parseAddressList(header.Get("Bcc"))
	email.Date = hp.parseTime(header.Get("Date"))
	email.ResentFrom = hp.parseAddressList(header.Get("Resent-From"))
	email.ResentSender = hp.parseAddress(header.Get("Resent-Sender"))
	email.ResentTo = hp.parseAddressList(header.Get("Resent-To"))
	email.ResentCc = hp.parseAddressList(header.Get("Resent-Cc"))
	email.ResentBcc = hp.parseAddressList(header.Get("Resent-Bcc"))
	email.ResentMessageID = hp.parseMessageId(header.Get("Resent-Message-ID"))
	email.MessageID = hp.parseMessageId(header.Get("Message-ID"))
	email.InReplyTo = hp.parseMessageIdList(header.Get("In-Reply-To"))
	email.References = hp.parseMessageIdList(header.Get("References"))
	email.ResentDate = hp.parseTime(header.Get("Resent-Date"))

	if hp.err != nil {
		err = hp.err
		return
	}

	//decode whole header for easier access to extra fields
	//todo: should we decode? aren't only standard fields mime encoded?
	email.Header, err = decodeHeaderMime(header)
	if err != nil {
		return
	}

	return
}

func parseContentType(contentTypeHeader string) (contentType string, params map[string]string, err error) {
	if contentTypeHeader == "" {
		contentType = contentTypeTextPlain
		return
	}

	return mime.ParseMediaType(contentTypeHeader)
}

func parseMultipartRelated(msg io.Reader, boundary string) (textBody, htmlBody string, attachments []Attachment, embeddedFiles []EmbeddedFile, textBodies []*TextBody, htmlBodies []*HTMLBody, err error) {
	pmr := multipart.NewReader(msg, boundary)
	for {
		part, err := NextPart(pmr)

		if err == io.EOF {
			break
		} else if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}

		contentType, params := part.contentType, part.contentTypeParams
		if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}

		switch contentType {
		case contentTypeTextPlain:
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBodies = append(textBodies, &TextBody{
				Body: *b,
			})
		case contentTypeTextHtml:
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}

			htmlBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBodies = append(htmlBodies, &HTMLBody{
				Body: *b,
			})
		case contentTypeTextCalendar:
			ef, err := decodeEmbeddedFile(part)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			embeddedFiles = append(embeddedFiles, ef)
		case contentTypeMultipartAlternative:
			tb, hb, af, ef, tbs, hbs, err := parseMultipartAlternative(part, params["boundary"])
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBody += hb
			textBody += tb
			attachments = append(attachments, af...)
			embeddedFiles = append(embeddedFiles, ef...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		default:
			if isEmbeddedFile(part) {
				ef, err := decodeEmbeddedFile(part)
				if err != nil {
					return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
				}

				embeddedFiles = append(embeddedFiles, ef)
			} else {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, fmt.Errorf("Can't process multipart/related inner mime type: %s", contentType)
			}
		}
	}

	return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
}

func parseMultipartAlternative(msg io.Reader, boundary string) (textBody, htmlBody string, attachments []Attachment, embeddedFiles []EmbeddedFile, textBodies []*TextBody, htmlBodies []*HTMLBody, err error) {
	pmr := multipart.NewReader(msg, boundary)
	for {
		part, err := NextPart(pmr)

		if err == io.EOF {
			break
		} else if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}

		contentType, params := part.contentType, part.contentTypeParams
		if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}

		switch contentType {
		case contentTypeTextPlain:
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBodies = append(textBodies, &TextBody{
				Body: *b,
			})
		case contentTypeTextHtml:
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBodies = append(htmlBodies, &HTMLBody{
				Body: *b,
			})
		case contentTypeTextCalendar:
			ef, err := decodeEmbeddedFile(part)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			embeddedFiles = append(embeddedFiles, ef)
		case contentTypeMultipartRelated:
			tb, hb, af, ef, tbs, hbs, err := parseMultipartRelated(part, params["boundary"])
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBody += hb
			textBody += tb
			attachments = append(attachments, af...)
			embeddedFiles = append(embeddedFiles, ef...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		case contentTypeMultipartMixed:
			tb, hb, at, ef, tbs, hbs, err := parseMultipartMixed(part, params["boundary"], 1)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBody += hb
			textBody += tb
			attachments = append(attachments, at...)
			embeddedFiles = append(embeddedFiles, ef...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		default:
			if strings.HasPrefix(contentType, contentTypeTextExtension) {
				continue
			}
			if isEmbeddedFile(part) {
				ef, err := decodeEmbeddedFile(part)
				if err != nil {
					return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
				}

				embeddedFiles = append(embeddedFiles, ef)
			} else {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, fmt.Errorf("Can't process multipart/alternative inner mime type: %s", contentType)
			}
		}
	}

	return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
}

func parseMultipartMixed(msg io.Reader, boundary string, depth int) (textBody, htmlBody string, attachments []Attachment, embeddedFiles []EmbeddedFile, textBodies []*TextBody, htmlBodies []*HTMLBody, err error) {
	if depth > maxDepthOfMultipartMixed {
		return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, fmt.Errorf("nested multiple/mixed above max depth")
	}
	mr := multipart.NewReader(msg, boundary)
	for {
		part, err := NextPart(mr)
		if err == io.EOF {
			break
		} else if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}
		if isAttachment(part) {
			at, err := decodeAttachment(part)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			attachments = append(attachments, at)
			continue
		}
		contentType, params := part.contentType, part.contentTypeParams
		if err != nil {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
		}
		if contentType == contentTypeMultipartAlternative {
			tb, hb, ats, efs, tbs, hbs, err := parseMultipartAlternative(part, params["boundary"])
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += tb
			htmlBody += hb
			attachments = append(attachments, ats...)
			embeddedFiles = append(embeddedFiles, efs...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		} else if contentType == contentTypeMultipartRelated {
			tb, hb, ats, efs, tbs, hbs, err := parseMultipartRelated(part, params["boundary"])
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += tb
			htmlBody += hb
			attachments = append(attachments, ats...)
			embeddedFiles = append(embeddedFiles, efs...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		} else if contentType == contentTypeMultipartMixed {
			tb, hb, ats, efs, tbs, hbs, err := parseMultipartMixed(part, params["boundary"], depth+1)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += tb
			hb += hb
			attachments = append(attachments, ats...)
			embeddedFiles = append(embeddedFiles, efs...)
			textBodies = append(textBodies, tbs...)
			htmlBodies = append(htmlBodies, hbs...)
		} else if contentType == contentTypeTextPlain {
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			textBodies = append(textBodies, &TextBody{
				Body: *b,
			})
		} else if contentType == contentTypeTextHtml {
			ppContent, err := ioutil.ReadAll(part.tee)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBody += strings.TrimSuffix(string(ppContent[:]), "\n")
			b, err := part.newBody()
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			htmlBodies = append(htmlBodies, &HTMLBody{
				Body: *b,
			})
		} else if contentType == contentTypeTextCalendar {
			ef, err := decodeEmbeddedFile(part)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			embeddedFiles = append(embeddedFiles, ef)
		} else if contentType == contentTypeApplicationOctetStream {
			at, err := decodeAttachment(part)
			if err != nil {
				return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
			}
			if at.Filename == "" {
				if name, ok := params["name"]; ok {
					at.Filename = decodeMimeSentence(name)
				}
			}
			attachments = append(attachments, at)
		} else {
			return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, fmt.Errorf("Unknown multipart/mixed nested mime type: %s", contentType)
		}
	}

	return textBody, htmlBody, attachments, embeddedFiles, textBodies, htmlBodies, err
}

func decodeMimeSentence(s string) string {
	result := []string{}
	ss := strings.Split(s, " ")

	for _, word := range ss {
		dec := new(mime.WordDecoder)
		w, err := dec.Decode(word)
		if err != nil {
			if len(result) == 0 {
				w = word
			} else {
				w = " " + word
			}
		}

		result = append(result, w)
	}

	return strings.Join(result, "")
}

func decodeHeaderMime(header mail.Header) (mail.Header, error) {
	parsedHeader := map[string][]string{}

	for headerName, headerData := range header {

		parsedHeaderData := []string{}
		for _, headerValue := range headerData {
			parsedHeaderData = append(parsedHeaderData, decodeMimeSentence(headerValue))
		}

		parsedHeader[headerName] = parsedHeaderData
	}

	return mail.Header(parsedHeader), nil
}

func isEmbeddedFile(part *Part) bool {
	return part.contentTransferEncoding != ""
}

func decodeEmbeddedFile(part *Part) (ef EmbeddedFile, err error) {
	cid := decodeMimeSentence(part.Header.Get("Content-Id"))
	decoded, err := decodeContent(part, part.contentTransferEncoding)
	if err != nil {
		return
	}

	ef.CID = strings.Trim(cid, "<>")
	ef.Data = decoded
	ef.ContentType = part.Header.Get("Content-Type")

	return
}

func isAttachment(part *Part) bool {
	return part.FileName() != "" || strings.ToLower(part.contentDisposition) == "attachment"
}

func decodeAttachment(part *Part) (at Attachment, err error) {
	filename := decodeMimeSentence(part.FileName())
	if filename == "" {
		if name, ok := part.contentTypeParams["name"]; ok {
			filename = decodeMimeSentence(name)
		}
	}
	decoded, err := decodeContent(part, part.Header.Get("Content-Transfer-Encoding"))
	if err != nil {
		return
	}

	at.Filename = filename
	at.Data = decoded
	at.ContentType = strings.Split(part.Header.Get("Content-Type"), ";")[0]

	return
}

func decodeContent(content io.Reader, encoding string) (io.Reader, error) {
	switch strings.ToLower(encoding) {
	case "quoted-printable":
		r := quotedprintable.NewReader(content)
		out := new(bytes.Buffer)
		_, err := io.Copy(out, r)
		if err != nil {
			return nil, err
		}
		return out, nil
	case "base64":
		decoded := base64.NewDecoder(base64.StdEncoding, content)
		b, err := ioutil.ReadAll(decoded)
		if err != nil {
			return nil, err
		}

		return bytes.NewReader(b), nil
	case "7bit", "8bit", "":
		dd, err := ioutil.ReadAll(content)
		if err != nil {
			return nil, err
		}

		return bytes.NewReader(dd), nil
	default:
		return nil, fmt.Errorf("unknown encoding: %s", encoding)
	}
}

type headerParser struct {
	header *mail.Header
	err    error
}

func (hp headerParser) parseAddress(s string) (ma *mail.Address) {
	if hp.err != nil {
		return nil
	}

	if strings.Trim(s, " \n") != "" {
		ma, hp.err = mail.ParseAddress(s)

		return ma
	}

	return nil
}

func (hp headerParser) parseAddressList(s string) (ma []*mail.Address) {
	if hp.err != nil {
		return
	}

	if strings.Trim(s, " \n") != "" {
		ma, hp.err = mail.ParseAddressList(s)
		return
	}

	return
}

func (hp headerParser) parseTime(s string) (t time.Time) {
	if hp.err != nil || s == "" {
		return
	}

	formats := []string{
		time.RFC1123Z,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		time.RFC1123Z + " (MST)",
		"Mon, 2 Jan 2006 15:04:05 -0700 (MST)",
	}

	for _, format := range formats {
		t, hp.err = time.Parse(format, s)
		if hp.err == nil {
			return
		}
	}

	return
}

func (hp headerParser) parseMessageId(s string) string {
	if hp.err != nil {
		return ""
	}

	return strings.Trim(s, "<> ")
}

func (hp headerParser) parseMessageIdList(s string) (result []string) {
	if hp.err != nil {
		return
	}

	for _, p := range strings.Split(s, " ") {
		if strings.Trim(p, " \n") != "" {
			result = append(result, hp.parseMessageId(p))
		}
	}

	return
}

// Attachment with filename, content type and data (as a io.Reader)
type Attachment struct {
	Filename    string
	ContentType string
	Data        io.Reader
}

// EmbeddedFile with content id, content type and data (as a io.Reader)
type EmbeddedFile struct {
	CID         string
	ContentType string
	Data        io.Reader
}

// Email with fields for all the headers defined in RFC5322 with it's attachments and
type Email struct {
	Header mail.Header

	Subject    string
	Sender     *mail.Address
	From       []*mail.Address
	ReplyTo    []*mail.Address
	To         []*mail.Address
	Cc         []*mail.Address
	Bcc        []*mail.Address
	Date       time.Time
	MessageID  string
	InReplyTo  []string
	References []string

	ResentFrom      []*mail.Address
	ResentSender    *mail.Address
	ResentTo        []*mail.Address
	ResentDate      time.Time
	ResentCc        []*mail.Address
	ResentBcc       []*mail.Address
	ResentMessageID string

	ContentType string
	Content     io.Reader

	HTMLBody string
	TextBody string

	Attachments   []Attachment
	EmbeddedFiles []EmbeddedFile

	HTMLBodies []*HTMLBody
	TextBodies []*TextBody
}

type Body struct {
	ContentType string
	Params      map[string]string
	Data        io.Reader
}

type HTMLBody struct {
	Body
}

type TextBody struct {
	Body
}

type Part struct {
	*multipart.Part
	contentType              string
	contentTypeParams        map[string]string
	contentDisposition       string
	contentDispositionParams map[string]string
	contentTransferEncoding  string
	tee                      io.Reader
	out                      *bytes.Buffer
}

func NextPart(r *multipart.Reader) (*Part, error) {
	p, err := r.NextPart()
	if err != nil {
		return nil, err
	}
	return newPart(p)
}

func newPart(part *multipart.Part) (out *Part, err error) {
	out = &Part{
		Part: part,
	}
	out.contentType, out.contentTypeParams, err = mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if part.Header.Get("Content-Disposition") != "" {
		out.contentDisposition, out.contentDispositionParams, err = mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			return nil, err
		}
	}
	out.contentTransferEncoding = part.Header.Get("Content-Transfer-Encoding")
	out.out = new(bytes.Buffer)
	out.tee = io.TeeReader(part, out.out)
	return out, nil
}

func (p *Part) newBody() (*Body, error) {
	data, err := decodeContent(p.out, p.contentTransferEncoding)
	if err != nil {
		return nil, err
	}
	return &Body{
		ContentType: p.contentType,
		Params:      p.contentTypeParams,
		Data:        data,
	}, nil
}

func (p *Part) FileName() string {
	if p.contentDispositionParams != nil {
		return p.contentDispositionParams["filename"]
	}

	return ""
}
