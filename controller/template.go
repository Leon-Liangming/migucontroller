package controller

import (
	text_template "text/template"
	"regexp"
	"strings"
	"encoding/json"
	"github.com/golang/glog"
	"bytes"
)

var (
	camelRegexp = regexp.MustCompile("[0-9A-Za-z]+")

	funcMap = text_template.FuncMap{
		"empty": func(input interface{}) bool {
			check, ok := input.(string)
			if ok {
				return len(check) == 0
			}

			return true
		},
		"contains":            strings.Contains,
		"hasPrefix":           strings.HasPrefix,
		"hasSuffix":           strings.HasSuffix,
		"toUpper":             strings.ToUpper,
		"toLower":             strings.ToLower,
	}
)

// Template ...
type Template struct {
	tmpl *text_template.Template
}

//NewTemplate returns a new Template instance or an
//error if the specified template file contains errors
func NewTemplate(file string, onChange func()) (*Template, error) {
	tmpl, err := text_template.New("upstream.tmpl").Funcs(funcMap).ParseFiles(file)
	if err != nil {
		return nil, err
	}

	return &Template{
		tmpl: tmpl,
	}, nil
}

// Write populates a buffer using a template with NGINX configuration
// and the servers and upstreams created by Ingress rules
func (t *Template) Write(upstreams []Upstream) ([]byte, error) {
	conf := make(map[string]interface{})
	conf["upstreams"] = upstreams
	if glog.V(3) {
		b, err := json.Marshal(conf)
		if err != nil {
			glog.Errorf("unexpected error: %v", err)
		}
		glog.Infof("NGINX configuration: %v", string(b))
	}

	buffer := new(bytes.Buffer)
	err := t.tmpl.Execute(buffer, conf)
	if err != nil {
		glog.V(3).Infof("%v", string(buffer.Bytes()))
		return nil, err
	}

	return buffer.Bytes(), nil
}