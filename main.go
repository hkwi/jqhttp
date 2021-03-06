package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/itchyny/gojq"
	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

var k = koanf.New(".")

func header_flatten(h http.Header) map[string]string {
	ret := make(map[string]string)
	for k, vs := range h {
		ret[k] = strings.Join(vs, ",")
	}
	return ret
}

func register_route(en *gin.Engine, rt *koanf.Koanf) error {
	var cin, cout *gojq.Code

	if rt.Exists("request") {
		if op, err := gojq.Parse(rt.String("request")); err != nil {
			return err
		} else if code, err := gojq.Compile(op); err != nil {
			return err
		} else {
			cin = code
		}
	}
	if rt.Exists("response") {
		if op, err := gojq.Parse(rt.String("response")); err != nil {
			return err
		} else if code, err := gojq.Compile(op); err != nil {
			return err
		} else {
			cout = code
		}
	}

	var upstream *url.URL
	if !rt.Exists("upstream") {
		return fmt.Errorf("upstream is missing")
	} else if u, err := url.Parse(rt.String("upstream")); err != nil {
		return err
	} else {
		upstream = u
	}

	pipe := func(c *gin.Context) error {
		var request_body io.Reader
		if cin == nil || c.Request.ContentLength == 0 {
			request_body = c.Request.Body
		} else {
			var data interface{}
			if body, err := c.GetRawData(); err != nil {
				return fmt.Errorf("request read %w", err)
			} else if err := json.Unmarshal(body, &data); err != nil {
				log.Printf("request json decode %v", err)
				request_body = bytes.NewReader(body)
				// fall through
			} else if tin, ok := cin.Run(data).Next(); !ok {
				return fmt.Errorf("jq run failed")
			} else if qin, err := gojq.Marshal(tin); err != nil {
				return fmt.Errorf("jq serialize %w", err)
			} else {
				request_body = bytes.NewReader(qin)
			}
		}

		dst := *upstream
		if suffix, ok := c.Params.Get("suffix"); ok {
			dst.Path = path.Join(dst.Path, suffix)
		}
		if req, err := http.NewRequest(c.Request.Method, dst.String(), request_body); err != nil {
			return fmt.Errorf("proxy build %w", err)
		} else {
			var res *http.Response

			req.Header = c.Request.Header.Clone()
			req.Header.Del("Accept-Encoding") // let the transport automatically set
			if rt.Exists("set.request.contenttype") {
				// workaround for the server/clients that can't change Content-type
				req.Header.Set("Content-Type", rt.String("set.request.contenttype"))
			}

			if r, err := http.DefaultClient.Do(req); err != nil {
				return fmt.Errorf("proxy request failed %w", err)
			} else {
				res = r
			}
			if rt.Exists("set.response.contenttype") {
				// workaround for the server/clients that can't change Content-type
				res.Header.Set("Content-Type", rt.String("set.response.contenttype"))
			}

			if cout == nil || res.ContentLength == 0 {
				// fall through
			} else {
				defer res.Body.Close()

				var data interface{}
				buf := bytes.NewBuffer(nil)

				if _, err := io.Copy(buf, res.Body); err != nil {
					return fmt.Errorf("proxy response read %w", err)
				} else if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
					log.Printf("response json decode %v", err)
					res.Body = ioutil.NopCloser(bytes.NewReader(buf.Bytes()))
					// fall through
				} else if t, ok := cout.Run(data).Next(); !ok {
					return fmt.Errorf("jq response run failed")
				} else if body, err := gojq.Marshal(t); err != nil {
					return fmt.Errorf("jq serialize %w", err)
				} else {
					c.DataFromReader(
						res.StatusCode,
						int64(len(body)),
						res.Header.Get("Content-Type"),
						ioutil.NopCloser(bytes.NewReader(body)),
						header_flatten(res.Header),
					)
					return nil
				}
			}
			c.DataFromReader(
				res.StatusCode,
				res.ContentLength,
				res.Header.Get("Content-Type"),
				res.Body,
				header_flatten(res.Header),
			)
			return nil
		}
	}

	path := rt.String("path")
	if strings.HasSuffix(path, "/") {
		path = path + "*suffix"
	}
	en.Any(path, func(c *gin.Context) {
		if err := pipe(c); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
	})
	return nil
}

func main() {
	yml_file := flag.String("c", "config.yml", "configuration yaml")
	flag.Parse()

	if err := k.Load(
		confmap.Provider(map[string]interface{}{
			"listen": ":8080",
		}, "."), nil,
	); err != nil {
		log.Fatalf("%v", err)
	}

	if err := k.Load(
		env.Provider("JQHTTP_", ".", func(s string) string {
			return strings.Replace(strings.ToLower(s), "_", ".", -1)
		}),
		nil,
	); err != nil {
		log.Fatalf("%v", err)
	}

	if _, err := os.Stat(*yml_file); os.IsNotExist(err) {
		// pass
	} else if err := k.Load(
		file.Provider(*yml_file),
		yaml.Parser(),
	); err != nil {
		log.Fatalf("%v", err)
	}

	en := gin.Default()
	if k.Exists("jqhttp") {
		if err := register_route(en, k.Cut("jqhttp")); err != nil {
			log.Fatal("%v", err)
		}
	}
	for _, rt := range k.Slices("routes") {
		if err := register_route(en, rt); err != nil {
			log.Fatal("%v", err)
		}
	}
	en.Run(k.String("listen"))
}
