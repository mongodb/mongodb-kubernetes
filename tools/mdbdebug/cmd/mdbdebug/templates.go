package main

import (
	"embed"
	"fmt"
	"strings"
	"sync"
	"text/template"
)

//go:embed templates/*
var templateFS embed.FS

type TemplateData struct {
	Namespace        string
	ResourceName     string
	ResourceType     string
	StsName          string
	PodName          string
	VolumeName       string
	PodIdx           int
	ClusterIdx       int
	ShortName        string
	TLSEnabled       bool
	PodFQDN          string
	StaticArch       bool
	ContainerName    string
	MongoDBCommunity bool
	BaseLogDir       string
}

var once sync.Once
var tmpl *template.Template

func renderTemplate(templateName string, data TemplateData) (string, error) {
	once.Do(func() {
		var err error
		tmpl, err = template.ParseFS(templateFS, fmt.Sprintf("templates/*.tpl"))
		if err != nil {
			panic(err)
		}
	})

	str := strings.Builder{}
	if err := tmpl.ExecuteTemplate(&str, templateName, data); err != nil {
		return "", err
	}

	return str.String(), nil
}
