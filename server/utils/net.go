package utils

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"server/config"
	"server/globals"
	"strings"

	"github.com/goccy/go-json"
	"golang.org/x/net/proxy"
)

func newClient(c []config.ProxyConfig) *http.Client {
	client := &http.Client{
		Timeout: globals.HttpMaxTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// 优先级：1.传入参数 2.配置文件 3.环境变量
	if len(c) == 0 {
		// 尝试从配置文件读取
		if globals.GraConf.System.Proxy != nil {
			c = append(c, *globals.GraConf.System.Proxy)
		} else if httpProxy := os.Getenv("HTTP_PROXY"); httpProxy != "" {
			c = append(c, config.ProxyConfig{
				ProxyType: config.HttpProxyType,
				Proxy:     httpProxy,
			})
		} else if httpsProxy := os.Getenv("HTTPS_PROXY"); httpsProxy != "" {
			c = append(c, config.ProxyConfig{
				ProxyType: config.HttpsProxyType,
				Proxy:     httpsProxy,
			})
		} else {
			return client
		}
	}

	proxyCfg := c[0]
	if proxyCfg.ProxyType == config.NoneProxyType {
		return client
	}

	if proxyCfg.ProxyType == config.HttpProxyType || proxyCfg.ProxyType == config.HttpsProxyType {
		proxyUrl, err := url.Parse(proxyCfg.Proxy)
		if len(proxyCfg.Username) > 0 || len(proxyCfg.Password) > 0 {
			proxyUrl.User = url.UserPassword(proxyCfg.Username, proxyCfg.Password)
		}

		if err != nil {
			globals.Warn(fmt.Sprintf("failed to parse proxy url: %s", err))
			return client
		}
		client.Transport = &http.Transport{
			Proxy:           http.ProxyURL(proxyUrl),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	} else if proxyCfg.ProxyType == config.Socks5ProxyType {
		var auth *proxy.Auth
		if len(proxyCfg.Username) > 0 || len(proxyCfg.Password) > 0 {
			auth = &proxy.Auth{
				User:     proxyCfg.Username,
				Password: proxyCfg.Password,
			}
		}

		dialer, err := proxy.SOCKS5("tcp", proxyCfg.Proxy, auth, proxy.Direct)
		if err != nil {
			globals.Warn(fmt.Sprintf("failed to create socks5 proxy: %s", err))
			return client
		}

		dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		}

		client.Transport = &http.Transport{
			DialContext:     dialContext,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	globals.Debug(fmt.Sprintf("[proxy] configured proxy: %s", proxyCfg.Proxy))
	return client
}

func fillHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}

func Http(uri string, method string, ptr interface{}, headers map[string]string, body io.Reader, proxyConfig []config.ProxyConfig) (err error) {
	if globals.DebugMode {
		globals.Debug(fmt.Sprintf("[http] %s %s\nheaders: \n%s\nbody: \n%s", method, uri, Marshal(headers), Marshal(body)))
	}

	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to create request: %s", err))
		}

		return err
	}
	fillHeaders(req, headers)

	client := newClient(proxyConfig)
	resp, err := client.Do(req)
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to send request: %s", err))
		}

		return err
	}

	defer resp.Body.Close()

	if err = json.NewDecoder(resp.Body).Decode(ptr); err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to decode response: %s\nresponse: %s", err, resp.Body))
		}

		return err
	}

	if globals.DebugMode {
		globals.Debug(fmt.Sprintf("[http] response: %s", Marshal(ptr)))
	}
	return nil
}

func HttpRaw(uri string, method string, headers map[string]string, body io.Reader, proxyConfig []config.ProxyConfig) (data []byte, err error) {
	if globals.DebugMode {
		globals.Debug(fmt.Sprintf("[http] %s %s\nheaders: \n%s\nbody: \n%s", method, uri, Marshal(headers), Marshal(body)))
	}

	req, err := http.NewRequest(method, uri, body)
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to create request: %s", err))
		}

		return nil, err
	}
	fillHeaders(req, headers)

	client := newClient(proxyConfig)
	resp, err := client.Do(req)
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to send request: %s", err))
		}

		return nil, err
	}

	defer resp.Body.Close()

	if data, err = io.ReadAll(resp.Body); err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http] failed to read response: %s", err))
		}

		return nil, err
	}

	if globals.DebugMode {
		globals.Debug(fmt.Sprintf("[http] response: %s", string(data)))
	}
	return data, nil
}

func Get(uri string, headers map[string]string, proxyConfig ...config.ProxyConfig) (data interface{}, err error) {
	err = Http(uri, http.MethodGet, &data, headers, nil, proxyConfig)
	return data, err
}

func GetRaw(uri string, headers map[string]string, proxyConfig ...config.ProxyConfig) (data string, err error) {
	buffer, err := HttpRaw(uri, http.MethodGet, headers, nil, proxyConfig)
	if err != nil {
		return "", err
	}
	return string(buffer), nil
}

func Post(uri string, headers map[string]string, body interface{}, proxyConfig ...config.ProxyConfig) (data interface{}, err error) {
	err = Http(uri, http.MethodPost, &data, headers, ConvertBody(body), proxyConfig)
	return data, err
}

func ToString(data interface{}) string {
	switch v := data.(type) {
	case string:
		return v
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		data := Marshal(data)
		if len(data) > 0 {
			return data
		}

		return fmt.Sprintf("%v", data)
	}
}

func PostRaw(uri string, headers map[string]string, body interface{}, proxyConfig ...config.ProxyConfig) (data string, err error) {
	buffer, err := HttpRaw(uri, http.MethodPost, headers, ConvertBody(body), proxyConfig)
	if err != nil {
		return "", err
	}
	return string(buffer), nil
}

func ConvertBody(body interface{}) (form io.Reader) {
	if buffer, err := json.Marshal(body); err == nil {
		form = bytes.NewBuffer(buffer)
	}
	return form
}

func EventSource(method string, uri string, headers map[string]string, body interface{}, callback func(string) error, proxyConfig ...config.ProxyConfig) error {
	// panic recovery
	defer func() {
		if err := recover(); err != nil {
			stack := debug.Stack()
			globals.Warn(fmt.Sprintf("event source panic: %s (uri: %s, method: %s)\n%s", err, uri, method, stack))
		}
	}()

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	if globals.DebugMode {
		globals.Debug(fmt.Sprintf("[http-stream] %s %s\nheaders: \n%s\nbody: \n%s", method, uri, Marshal(headers), Marshal(body)))
	}

	client := newClient(proxyConfig)
	req, err := http.NewRequest(method, uri, ConvertBody(body))
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http-stream] failed to create request: %s", err))
		}

		return err
	}

	fillHeaders(req, headers)

	res, err := client.Do(req)
	if err != nil {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http-stream] failed to send request: %s", err))
		}

		return err
	}

	defer res.Body.Close()

	if res.StatusCode >= 400 {
		if globals.DebugMode {
			globals.Debug(fmt.Sprintf("[http-stream] request failed with status: %s\nresponse: %s", res.Status, res.Body))
		}

		if content, err := io.ReadAll(res.Body); err == nil {
			if form, err := Unmarshal[map[string]interface{}](content); err == nil {
				data := MarshalWithIndent(form, 2)
				return fmt.Errorf("request failed with status: %s\n```json\n%s\n```", res.Status, data)
			}
		}

		return fmt.Errorf("request failed with status: %s", res.Status)
	}

	for {
		buf := make([]byte, 20480)
		n, err := res.Body.Read(buf)

		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		data := string(buf[:n])
		for _, item := range strings.Split(data, "\n") {
			if globals.DebugMode {
				globals.Debug(fmt.Sprintf("[http-stream] response: %s", item))
			}

			segment := strings.TrimSpace(item)
			if len(segment) > 0 {
				if err := callback(segment); err != nil {
					return err
				}
			}
		}
	}
}
