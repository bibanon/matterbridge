package helper

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/image/webp"

	"github.com/42wim/matterbridge/bridge/config"
	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/sirupsen/logrus"
)

// DownloadFile downloads the given non-authenticated URL.
func DownloadFile(url string) (*[]byte, error) {
	return DownloadFileAuth(url, "")
}

// DownloadFileAuth downloads the given URL using the specified authentication token.
func DownloadFileAuth(url string, auth string) (*[]byte, error) {
	var buf bytes.Buffer
	client := &http.Client{
		Timeout: time.Second * 5,
	}
	req, err := http.NewRequest("GET", url, nil)
	if auth != "" {
		req.Header.Add("Authorization", auth)
	}
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		peek, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected HTTP status: %s, body: %.100s", resp.Status, peek)
	}
	io.Copy(&buf, resp.Body)
	data := buf.Bytes()
	return &data, nil
}

// DownloadFileAuthRocket downloads the given URL using the specified Rocket user ID and authentication token.
func DownloadFileAuthRocket(url, token, userID string) (*[]byte, error) {
	var buf bytes.Buffer
	client := &http.Client{
		Timeout: time.Second * 5,
	}
	req, err := http.NewRequest("GET", url, nil)

	req.Header.Add("X-Auth-Token", token)
	req.Header.Add("X-User-Id", userID)

	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(&buf, resp.Body)
	data := buf.Bytes()
	return &data, err
}

// GetSubLines splits messages in newline-delimited lines. If maxLineLength is
// specified as non-zero GetSubLines will also clip long lines to the maximum
// length and insert a warning marker that the line was clipped.
//
// TODO: The current implementation has the inconvenient that it disregards
// word boundaries when splitting but this is hard to solve without potentially
// breaking formatting and other stylistic effects.
func GetSubLines(message string, maxLineLength int, clippingMessage string) []string {
	if clippingMessage == "" {
		clippingMessage = " <clipped message>"
	}

	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(message), "\n") {
		if line == "" {
			// Prevent sending empty messages, so we'll skip this line
			// if it has no content.
			continue
		}

		if maxLineLength == 0 || len([]byte(line)) <= maxLineLength {
			lines = append(lines, line)
			continue
		}

		// !!! WARNING !!!
		// Before touching the splitting logic below please ensure that you PROPERLY
		// understand how strings, runes and range loops over strings work in Go.
		// A good place to start is to read https://blog.golang.org/strings. :-)
		var splitStart int
		var startOfPreviousRune int
		for i := range line {
			if i-splitStart > maxLineLength-len([]byte(clippingMessage)) {
				lines = append(lines, line[splitStart:startOfPreviousRune]+clippingMessage)
				splitStart = startOfPreviousRune
			}
			startOfPreviousRune = i
		}
		// This last append is safe to do without looking at the remaining byte-length
		// as we assume that the byte-length of the last rune will never exceed that of
		// the byte-length of the clipping message.
		lines = append(lines, line[splitStart:])
	}
	return lines
}

// HandleExtra manages the supplementary details stored inside a message's 'Extra' field map.
func HandleExtra(msg *config.Message, general *config.Protocol) []config.Message {
	extra := msg.Extra
	rmsg := []config.Message{}
	for _, f := range extra[config.EventFileFailureSize] {
		fi := f.(config.FileInfo)
		text := fmt.Sprintf("file %s too big to download (%#v > allowed size: %#v)", fi.Name, fi.Size, general.MediaDownloadSize)
		rmsg = append(rmsg, config.Message{
			Text:     text,
			Username: "<system> ",
			Channel:  msg.Channel,
			Account:  msg.Account,
		})
	}
	return rmsg
}

// GetAvatar constructs a URL for a given user-avatar if it is available in the cache.
func GetAvatar(av map[string]string, userid string, general *config.Protocol) string {
	if sha, ok := av[userid]; ok {
		return general.MediaServerDownload + "/" + sha + "/" + userid + ".png"
	}
	return ""
}

// HandleDownloadSize checks a specified filename against the configured download blacklist
// and checks a specified file-size against the configure limit.
func HandleDownloadSize(logger *logrus.Entry, msg *config.Message, name string, size int64, general *config.Protocol) error {
	// check blacklist here
	for _, entry := range general.MediaDownloadBlackList {
		if entry != "" {
			re, err := regexp.Compile(entry)
			if err != nil {
				logger.Errorf("incorrect regexp %s for %s", entry, msg.Account)
				continue
			}
			if re.MatchString(name) {
				return fmt.Errorf("Matching blacklist %s. Not downloading %s", entry, name)
			}
		}
	}
	logger.Debugf("Trying to download %#v with size %#v", name, size)
	if int(size) > general.MediaDownloadSize {
		msg.Event = config.EventFileFailureSize
		msg.Extra[msg.Event] = append(msg.Extra[msg.Event], config.FileInfo{
			Name:    name,
			Comment: msg.Text,
			Size:    size,
		})
		return fmt.Errorf("File %#v to large to download (%#v). MediaDownloadSize is %#v", name, size, general.MediaDownloadSize)
	}
	return nil
}

// HandleDownloadData adds the data for a remote file into a Matterbridge gateway message.
func HandleDownloadData(logger *logrus.Entry, msg *config.Message, name, comment, url string, data *[]byte, general *config.Protocol) {
	HandleDownloadData2(logger, msg, name, "", comment, url, data, general)
}

// HandleDownloadData adds the data for a remote file into a Matterbridge gateway message.
func HandleDownloadData2(logger *logrus.Entry, msg *config.Message, name, id, comment, url string, data *[]byte, general *config.Protocol) {
	var avatar bool
	logger.Debugf("Download OK %#v %#v", name, len(*data))
	if msg.Event == config.EventAvatarDownload {
		avatar = true
	}
	msg.Extra["file"] = append(msg.Extra["file"], config.FileInfo{
		Name:     name,
		Data:     data,
		URL:      url,
		Comment:  comment,
		Avatar:   avatar,
		NativeID: id,
	})
}

var emptyLineMatcher = regexp.MustCompile("\n+")

// RemoveEmptyNewLines collapses consecutive newline characters into a single one and
// trims any preceding or trailing newline characters as well.
func RemoveEmptyNewLines(msg string) string {
	return emptyLineMatcher.ReplaceAllString(strings.Trim(msg, "\n"), "\n")
}

// ClipMessage trims a message to the specified length if it exceeds it and adds a warning
// to the message in case it does so.
func ClipMessage(text string, length int, clippingMessage string) string {
	if clippingMessage == "" {
		clippingMessage = " <clipped message>"
	}

	if len(text) > length {
		text = text[:length-len(clippingMessage)]
		for len(text) > 0 {
			if r, _ := utf8.DecodeLastRuneInString(text); r == utf8.RuneError {
				text = text[:len(text)-1]
				// Note: DecodeLastRuneInString only returns the constant value "1" in
				// case of an error. We do not yet know whether the last rune is now
				// actually valid. Example: "€" is 0xE2 0x82 0xAC. If we happen to split
				// the string just before 0xAC, and go back only one byte, that would
				// leave us with a string that ends in the byte 0xE2, which is not a valid
				// rune, so we need to try again.
			} else {
				break
			}
		}
		text += clippingMessage
	}
	return text
}

func ClipOrSplitMessage(text string, length int, clippingMessage string, splitMax int) []string {
	var msgParts []string
	remainingText := text
	// Invariant of this splitting loop: No text is lost (msgParts+remainingText is the original text),
	// and all parts is guaranteed to satisfy the length requirement.
	for len(msgParts) < splitMax-1 && len(remainingText) > length {
		// Decision: The text needs to be split (again).
		var chunk string
		wasted := 0
		// The longest UTF-8 encoding of a valid rune is 4 bytes (0xF4 0x8F 0xBF 0xBF, encoding U+10FFFF),
		// so we should never need to waste 4 or more bytes at a time.
		for wasted < 4 && wasted < length {
			chunk = remainingText[:length-wasted]
			if r, _ := utf8.DecodeLastRuneInString(chunk); r == utf8.RuneError {
				wasted += 1
			} else {
				break
			}
		}
		// Note: At this point, "chunk" might still be invalid, if "text" is very broken.
		msgParts = append(msgParts, chunk)
		remainingText = remainingText[len(chunk):]
	}
	msgParts = append(msgParts, ClipMessage(remainingText, length, clippingMessage))
	return msgParts
}

// ParseMarkdown takes in an input string as markdown and parses it to html
func ParseMarkdown(input string) string {
	extensions := parser.HardLineBreak | parser.NoIntraEmphasis | parser.FencedCode
	markdownParser := parser.NewWithExtensions(extensions)
	renderer := html.NewRenderer(html.RendererOptions{
		Flags: 0,
	})
	parsedMarkdown := markdown.ToHTML([]byte(input), markdownParser, renderer)
	res := string(parsedMarkdown)
	res = strings.TrimPrefix(res, "<p>")
	res = strings.TrimSuffix(res, "</p>\n")
	return res
}

// ConvertWebPToPNG converts input data (which should be WebP format) to PNG format
func ConvertWebPToPNG(data *[]byte) error {
	r := bytes.NewReader(*data)
	m, err := webp.Decode(r)
	if err != nil {
		return err
	}
	var output []byte
	w := bytes.NewBuffer(output)
	if err := png.Encode(w, m); err != nil {
		return err
	}
	*data = w.Bytes()
	return nil
}
