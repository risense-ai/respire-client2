package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
)

// modelReleaseSourceError labels the canonical URL before redirects. Preserve
// rich causes for error classification, but do not display transport URLs,
// proxy credentials, response headers, or status text supplied by a relay.
type modelReleaseSourceError struct {
	url   string
	cause error
}

func (e *modelReleaseSourceError) Unwrap() error { return e.cause }

func (e *modelReleaseSourceError) Error() string {
	return e.url + ": " + modelReleaseFailureDetail(e.cause)
}

func modelReleaseFailureDetail(err error) string {
	var status *modelHTTPError
	var digest *modelIntegrityError
	var dns *net.DNSError
	var network net.Error
	var invalid *modelRangeError
	var disk *os.PathError
	switch {
	case errors.As(err, &status):
		origin := ""
		if status.proxy {
			origin = "代理 "
		}
		return fmt.Sprintf("%sHTTP %d", origin, status.status)
	case errors.Is(err, errCorruptModelCleanup):
		return errCorruptModelCleanup.Error()
	case errors.As(err, &digest):
		return "大小或 SHA-256 校验不匹配"
	case errors.Is(err, errModelHeaderStall):
		return errModelHeaderStall.Error()
	case errors.Is(err, errModelReadStall):
		return errModelReadStall.Error()
	case errors.Is(err, context.Canceled):
		return "下载已取消"
	case errors.Is(err, context.DeadlineExceeded):
		return "请求已超时"
	case errors.As(err, &dns):
		return "无法解析下载域名"
	case errors.As(err, &network) && network.Timeout():
		return "网络请求超时"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "下载流提前结束"
	case errors.As(err, &invalid):
		return "分段下载响应或暂存写入无效"
	case errors.As(err, &disk):
		return "无法读取或写入模型文件"
	default:
		return "无法完成下载请求"
	}
}
