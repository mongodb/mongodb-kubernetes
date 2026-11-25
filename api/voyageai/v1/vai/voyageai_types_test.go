package vai

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSupportedVoyageAIModelsMatchKubebuilderEnum(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source file")
	}
	sourceFile := filepath.Join(filepath.Dir(testFile), "voyageai_types.go")
	file, err := parser.ParseFile(token.NewFileSet(), sourceFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse VoyageAI API source: %v", err)
	}

	const enumMarker = "+kubebuilder:validation:Enum="
	var enumValues []string
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			modelType, ok := spec.(*ast.TypeSpec)
			if !ok || modelType.Name.Name != "VoyageAIModel" {
				continue
			}
			for _, group := range []*ast.CommentGroup{gen.Doc, modelType.Doc} {
				if group == nil {
					continue
				}
				for _, comment := range group.List {
					value, found := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(comment.Text, "//")), enumMarker)
					if found {
						enumValues = strings.Split(value, ";")
					}
				}
			}
		}
	}
	if enumValues == nil {
		t.Fatalf("missing %q marker on VoyageAIModel", enumMarker)
	}

	models := SupportedVoyageAIModels()
	modelValues := make([]string, len(models))
	for i, model := range models {
		modelValues[i] = string(model)
	}
	if !reflect.DeepEqual(enumValues, modelValues) {
		t.Fatalf("VoyageAIModel Kubebuilder enum %v does not match SupportedVoyageAIModels %v", enumValues, modelValues)
	}
}
