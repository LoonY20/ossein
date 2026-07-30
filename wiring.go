package ossein

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

// GenerateWiring emits a Go source file that constructs every registered
// service with explicit constructor calls in dependency order, removing
// container reflection from application startup.
//
// Singleton services become fields of a generated Services struct built by
// Wire. Instance registrations become Wire parameters, because generated code
// cannot reproduce runtime values. Transient registrations become factory
// methods on Services.
//
// Constructors must be exported top-level functions outside package main, and
// every registration type must be exported. Generation reports registrations
// that cannot be expressed as source code instead of guessing.
func GenerateWiring(app *App, packageName string) ([]byte, error) {
	if app == nil {
		return nil, fmt.Errorf("ossein wiring: app cannot be nil")
	}
	if packageName == "" {
		return nil, fmt.Errorf("ossein wiring: package name cannot be empty")
	}
	if err := app.services.Validate(); err != nil {
		return nil, fmt.Errorf("ossein wiring: %w", err)
	}

	generator := newWiringGenerator(packageName)
	if err := generator.collect(app.services); err != nil {
		return nil, err
	}
	return generator.render()
}

// WriteWiringFile generates wiring source and writes it to path, creating
// parent directories when needed.
func WriteWiringFile(app *App, path, packageName string) error {
	source, err := GenerateWiring(app, packageName)
	if err != nil {
		return err
	}
	path = filepath.FromSlash(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ossein wiring: create output directory: %w", err)
	}
	if err := os.WriteFile(path, source, 0o644); err != nil {
		return fmt.Errorf("ossein wiring: write %s: %w", path, err)
	}
	return nil
}

type wiringService struct {
	key        reflect.Type
	fieldName  string
	varName    string
	typeSource string
	inputs     []reflect.Type
	lifetime   Lifetime

	// constructorSource is empty for Instance registrations.
	constructorSource string
	// constructorErr reports that the constructor returns (T, error).
	constructorErr bool
	// factoryErr reports that a transient factory method returns an error,
	// either from its own constructor or a transient dependency chain.
	factoryErr bool
}

func (s *wiringService) isInstance() bool {
	return s.constructorSource == ""
}

type wiringGenerator struct {
	packageName string
	services    map[reflect.Type]*wiringService
	ordered     []*wiringService
	imports     map[string]string
	aliases     map[string]bool
}

func newWiringGenerator(packageName string) *wiringGenerator {
	return &wiringGenerator{
		packageName: packageName,
		imports:     map[string]string{},
		// Reserve fmt so error wrapping can always import it without an
		// alias collision.
		aliases: map[string]bool{"fmt": true},
	}
}

func (g *wiringGenerator) collect(container *Container) error {
	registrations := container.snapshot()

	keys := make([]reflect.Type, 0, len(registrations))
	for key := range registrations {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	g.services = make(map[reflect.Type]*wiringService, len(keys))
	usedFields := map[string]reflect.Type{}
	for _, key := range keys {
		registration := registrations[key]
		service := &wiringService{
			key:      key,
			inputs:   registration.inputs,
			lifetime: registration.lifetime,
		}

		fieldName, err := wiringFieldName(key)
		if err != nil {
			return err
		}
		if previous, exists := usedFields[fieldName]; exists {
			return fmt.Errorf(
				"ossein wiring: services %s and %s both map to field %s; rename one type",
				previous, key, fieldName,
			)
		}
		usedFields[fieldName] = key
		service.fieldName = fieldName
		service.varName = string(unicode.ToLower(rune(fieldName[0]))) + fieldName[1:]

		typeSource, err := g.typeSource(key)
		if err != nil {
			return fmt.Errorf("ossein wiring: service %s: %w", key, err)
		}
		service.typeSource = typeSource

		if registration.constructor.IsValid() {
			symbol, err := g.constructorSource(registration.constructor)
			if err != nil {
				return fmt.Errorf("ossein wiring: service %s: %w", key, err)
			}
			service.constructorSource = symbol
			service.constructorErr = registration.constructor.Type().NumOut() == 2
		}

		g.services[key] = service
	}

	for _, key := range keys {
		service := g.services[key]
		if service.isInstance() || service.lifetime != SingletonLifetime {
			continue
		}
		for _, input := range service.inputs {
			if g.services[input].lifetime == TransientLifetime {
				return fmt.Errorf(
					"ossein wiring: singleton %s depends on transient %s; generated wiring does not support this",
					key, input,
				)
			}
		}
	}

	// Emit singleton constructors in dependency order.
	visited := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(key reflect.Type) {
		if visited[key] {
			return
		}
		visited[key] = true
		service := g.services[key]
		for _, input := range service.inputs {
			visit(input)
		}
		if !service.isInstance() && service.lifetime == SingletonLifetime {
			g.ordered = append(g.ordered, service)
		}
	}
	for _, key := range keys {
		if g.services[key].lifetime == SingletonLifetime {
			visit(key)
		}
	}

	// A transient factory returns an error when its own constructor does, or
	// when any transient dependency chain does.
	resolved := map[reflect.Type]bool{}
	var factoryErr func(reflect.Type) bool
	factoryErr = func(key reflect.Type) bool {
		service := g.services[key]
		if service.lifetime != TransientLifetime {
			return false
		}
		if value, ok := resolved[key]; ok {
			return value
		}
		value := service.constructorErr
		for _, input := range service.inputs {
			if factoryErr(input) {
				value = true
			}
		}
		resolved[key] = value
		return value
	}
	for _, key := range keys {
		service := g.services[key]
		if service.lifetime == TransientLifetime {
			service.factoryErr = factoryErr(key)
		}
	}

	return nil
}

func (g *wiringGenerator) render() ([]byte, error) {
	var body strings.Builder

	var fields, instances, transients []*wiringService
	for _, service := range g.services {
		switch {
		case service.lifetime == TransientLifetime:
			transients = append(transients, service)
		case service.isInstance():
			fields = append(fields, service)
			instances = append(instances, service)
		default:
			fields = append(fields, service)
		}
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].fieldName < fields[j].fieldName })
	sort.Slice(instances, func(i, j int) bool { return instances[i].varName < instances[j].varName })
	sort.Slice(transients, func(i, j int) bool { return transients[i].fieldName < transients[j].fieldName })

	body.WriteString("// Services holds every singleton service wired at startup.\n")
	body.WriteString("type Services struct {\n")
	for _, service := range fields {
		fmt.Fprintf(&body, "\t%s %s\n", service.fieldName, service.typeSource)
	}
	body.WriteString("}\n\n")

	parameters := make([]string, len(instances))
	for i, service := range instances {
		parameters[i] = service.varName + " " + service.typeSource
	}
	body.WriteString("// Wire constructs every singleton service in dependency order.\n")
	fmt.Fprintf(&body, "func Wire(%s) (*Services, error) {\n", strings.Join(parameters, ", "))
	for _, service := range g.ordered {
		arguments := g.argumentList(service.inputs)
		if service.constructorErr {
			fmt.Fprintf(&body, "\t%s, err := %s(%s)\n", service.varName, service.constructorSource, arguments)
			body.WriteString("\tif err != nil {\n")
			fmt.Fprintf(&body, "\t\treturn nil, fmt.Errorf(\"wire %s: %%w\", err)\n", service.typeSource)
			body.WriteString("\t}\n")
		} else {
			fmt.Fprintf(&body, "\t%s := %s(%s)\n", service.varName, service.constructorSource, arguments)
		}
	}
	if len(fields) == 0 {
		body.WriteString("\treturn &Services{}, nil\n}\n")
	} else {
		body.WriteString("\treturn &Services{\n")
		for _, service := range fields {
			fmt.Fprintf(&body, "\t\t%s: %s,\n", service.fieldName, service.varName)
		}
		body.WriteString("\t}, nil\n}\n")
	}

	for _, service := range transients {
		g.renderFactory(&body, service)
	}

	if strings.Contains(body.String(), "fmt.Errorf") {
		g.imports["fmt"] = "fmt"
	}

	var file strings.Builder
	file.WriteString("// Code generated by ossein wire. DO NOT EDIT.\n\n")
	fmt.Fprintf(&file, "package %s\n\n", g.packageName)

	importPaths := make([]string, 0, len(g.imports))
	for path := range g.imports {
		importPaths = append(importPaths, path)
	}
	sort.Strings(importPaths)
	if len(importPaths) > 0 {
		file.WriteString("import (\n")
		for _, path := range importPaths {
			fmt.Fprintf(&file, "\t%s %q\n", g.imports[path], path)
		}
		file.WriteString(")\n\n")
	}
	file.WriteString(body.String())

	source, err := format.Source([]byte(file.String()))
	if err != nil {
		return nil, fmt.Errorf("ossein wiring: format generated source: %w", err)
	}
	return source, nil
}

func (g *wiringGenerator) renderFactory(body *strings.Builder, service *wiringService) {
	fmt.Fprintf(body, "\n// New%s builds a new transient %s.\n", service.fieldName, service.typeSource)
	if service.factoryErr {
		fmt.Fprintf(body, "func (s *Services) New%s() (%s, error) {\n", service.fieldName, service.typeSource)
	} else {
		fmt.Fprintf(body, "func (s *Services) New%s() %s {\n", service.fieldName, service.typeSource)
	}

	arguments := make([]string, len(service.inputs))
	for i, input := range service.inputs {
		dependency := g.services[input]
		if dependency.lifetime != TransientLifetime {
			arguments[i] = "s." + dependency.fieldName
			continue
		}
		if dependency.factoryErr {
			fmt.Fprintf(body, "\t%s, err := s.New%s()\n", dependency.varName, dependency.fieldName)
			body.WriteString("\tif err != nil {\n")
			fmt.Fprintf(
				body,
				"\t\treturn *new(%s), fmt.Errorf(\"wire %s: %%w\", err)\n",
				service.typeSource, dependency.typeSource,
			)
			body.WriteString("\t}\n")
		} else {
			fmt.Fprintf(body, "\t%s := s.New%s()\n", dependency.varName, dependency.fieldName)
		}
		arguments[i] = dependency.varName
	}

	call := fmt.Sprintf("%s(%s)", service.constructorSource, strings.Join(arguments, ", "))
	switch {
	case service.constructorErr:
		fmt.Fprintf(body, "\treturn %s\n}\n", call)
	case service.factoryErr:
		fmt.Fprintf(body, "\treturn %s, nil\n}\n", call)
	default:
		fmt.Fprintf(body, "\treturn %s\n}\n", call)
	}
}

func (g *wiringGenerator) argumentList(inputs []reflect.Type) string {
	arguments := make([]string, len(inputs))
	for i, input := range inputs {
		arguments[i] = g.services[input].varName
	}
	return strings.Join(arguments, ", ")
}

// constructorSource resolves a constructor to package-qualified source such
// as "service.NewUserService", registering the required import.
func (g *wiringGenerator) constructorSource(constructor reflect.Value) (string, error) {
	symbol := runtime.FuncForPC(constructor.Pointer())
	if symbol == nil {
		return "", fmt.Errorf("constructor symbol cannot be resolved")
	}
	full := symbol.Name()

	slash := strings.LastIndexByte(full, '/')
	dot := strings.IndexByte(full[slash+1:], '.')
	if dot < 0 {
		return "", fmt.Errorf("constructor %s must be a named top-level function", full)
	}
	pkgPath := full[:slash+1+dot]
	name := full[slash+1+dot+1:]

	if name == "" || strings.ContainsAny(name, ".-*()") {
		return "", fmt.Errorf(
			"constructor %s must be a named top-level function, not a closure or method",
			full,
		)
	}
	if !unicode.IsUpper(rune(name[0])) {
		return "", fmt.Errorf("constructor %s must be exported", full)
	}
	if pkgPath == "main" {
		return "", fmt.Errorf(
			"constructor %s lives in package main; move it to an importable package",
			full,
		)
	}

	return g.importAlias(pkgPath) + "." + name, nil
}

// typeSource renders a registration type as Go source, registering imports.
func (g *wiringGenerator) typeSource(t reflect.Type) (string, error) {
	switch t.Kind() {
	case reflect.Pointer:
		element, err := g.typeSource(t.Elem())
		if err != nil {
			return "", err
		}
		return "*" + element, nil
	case reflect.Slice:
		element, err := g.typeSource(t.Elem())
		if err != nil {
			return "", err
		}
		return "[]" + element, nil
	case reflect.Map:
		key, err := g.typeSource(t.Key())
		if err != nil {
			return "", err
		}
		value, err := g.typeSource(t.Elem())
		if err != nil {
			return "", err
		}
		return "map[" + key + "]" + value, nil
	}

	name := t.Name()
	if name == "" {
		return "", fmt.Errorf("type %s cannot be expressed in generated wiring", t)
	}
	if strings.ContainsRune(name, '[') {
		return "", fmt.Errorf("generic type %s is not supported in generated wiring", t)
	}
	if t.PkgPath() == "" {
		return name, nil
	}
	if t.PkgPath() == "main" {
		return "", fmt.Errorf("type %s lives in package main; move it to an importable package", t)
	}
	if !unicode.IsUpper(rune(name[0])) {
		return "", fmt.Errorf("type %s must be exported for generated wiring", t)
	}
	return g.importAlias(t.PkgPath()) + "." + name, nil
}

// importAlias registers pkgPath and returns a stable alias. Aliases are
// always written explicitly because reflection cannot recover a package's
// declared name from its import path.
func (g *wiringGenerator) importAlias(pkgPath string) string {
	if alias, ok := g.imports[pkgPath]; ok {
		return alias
	}

	segments := strings.Split(pkgPath, "/")
	base := segments[len(segments)-1]
	if len(segments) > 1 && len(base) > 1 && base[0] == 'v' && isAllDigits(base[1:]) {
		base = segments[len(segments)-2]
	}
	alias := sanitizeIdentifier(base)
	if alias == "" {
		alias = "pkg"
	}
	candidate := alias
	for suffix := 2; g.aliases[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s%d", alias, suffix)
	}
	g.imports[pkgPath] = candidate
	g.aliases[candidate] = true
	return candidate
}

// wiringFieldName derives the Services field name from a registration type.
func wiringFieldName(key reflect.Type) (string, error) {
	base := key
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	name := base.Name()
	if name == "" || strings.ContainsRune(name, '[') {
		return "", fmt.Errorf(
			"ossein wiring: service %s cannot be named as a Services field",
			key,
		)
	}
	return strings.ToUpper(name[:1]) + name[1:], nil
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sanitizeIdentifier(value string) string {
	var result strings.Builder
	for i, r := range value {
		switch {
		case unicode.IsLetter(r) || r == '_':
			result.WriteRune(r)
		case unicode.IsDigit(r) && i > 0:
			result.WriteRune(r)
		}
	}
	return result.String()
}
