package driver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/stephenafamo/bob/gen/drivers"
	"github.com/stephenafamo/bob/gen/importers"
	"github.com/takuoki/gocase"
)

type (
	Interface = drivers.Interface[Extra]
	DBInfo    = drivers.DBInfo[Extra]
	Provider  struct {
		DriverName      string
		DriverPkg       string
		DriverSource    string
		DriverENVSource string
	}
	Extra struct {
		Provider Provider
	}
	Config struct {
		// List of tables that will be included. Others are ignored
		Only map[string][]string
		// List of tables that will be should be ignored. Others are included
		Except map[string][]string

		// The name you wish to assign to your generated models package
		Pkgname   string
		NoFactory bool `yaml:"no_factory"`
	}
)

func New(config Config, dialect string, provider Provider, datamodel Datamodel) Interface {
	if config.Pkgname == "" {
		config.Pkgname = "prisma"
	}
	return &driver{
		dialect:   dialect,
		config:    config,
		provider:  provider,
		datamodel: datamodel,
	}
}

// driver holds the database connection string and a handle
// to the database connection.
type driver struct {
	dialect   string
	config    Config
	enums     map[string]drivers.Enum
	provider  Provider
	datamodel Datamodel
}

func (d *driver) Dialect() string {
	return d.dialect
}

func (d *driver) PackageName() string {
	return d.config.Pkgname
}

func (d *driver) Capabilities() drivers.Capabilities {
	return drivers.Capabilities{
		BulkInsert: d.dialect != "mysql",
	}
}

// Assemble all the information we need to provide back to the driver
func (d *driver) Assemble(_ context.Context) (*DBInfo, error) {
	var dbinfo *DBInfo
	var err error

	// drivers.Tables call translateColumnType which uses Enums
	d.loadEnums()

	dbinfo = &DBInfo{
		Tables: d.tables(),
		ExtraInfo: Extra{
			Provider: d.provider,
		},
		Enums: d.getEnums(),
	}

	return dbinfo, err
}

func (d *driver) tables() []drivers.Table {
	models := d.datamodel.Models
	tables := make([]drivers.Table, 0, len(models))

	tblFilter := drivers.ParseTableFilter(d.config.Only, d.config.Except)

	colFilter := drivers.ParseColumnFilter(d.tableNames(drivers.Filter{
		Only:   tblFilter.Only,
		Except: tblFilter.Except,
	}), d.config.Only, d.config.Except)

	for _, model := range models {
		if drivers.Skip(model.TableName(), tblFilter.Only, tblFilter.Except) {
			continue
		}

		pk, uniques, fks := d.getKeys(model, colFilter)

		table := drivers.Table{
			Key:     model.TableName(),
			Name:    model.TableName(),
			Columns: d.tableColumns(model, colFilter),
			PKey:    pk,
			Uniques: uniques,
			FKeys:   fks,
		}
		table.IsJoinTable = drivers.IsJoinTable(table)

		tables = append(tables, table)
	}

	relationships := drivers.BuildRelationships(tables)
	for i, t := range tables {
		tables[i].Relationships = relationships[t.Key]
	}

	// This just sets the default Alias of relationships based on the field name
	// we do this after building the relationships based on the keys
	for _, model := range models {
		if drivers.Skip(model.TableName(), tblFilter.Only, tblFilter.Except) {
			continue
		}

		var tableIndex int
		var table drivers.Table
		for i, t := range tables {
			if t.Key == model.TableName() {
				tableIndex = i
				table = t
				break
			}
		}

		tableName := model.TableName()
		allfilter := colFilter["*"]
		filter := colFilter[tableName]
		include := append(allfilter.Only, filter.Only...)
		exclude := append(allfilter.Except, filter.Except...)
		for _, field := range model.Fields {
			if drivers.Skip(field.Name, include, exclude) {
				continue
			}

			if field.Kind == FieldKindObject {
				for i, rel := range table.Relationships {
					if rel.Name == field.RelationName {
						tables[tableIndex].Relationships[i].Alias = strcase.ToCamel(field.Name)
					}
				}
			}
		}
	}

	return tables
}

// tableNames connects to the postgres database and
// retrieves all table names from the information_schema where the
// table schema is schema. It uses a whitelist and blacklist.
func (d *driver) tableNames(tableFilter drivers.Filter) []string {
	models := d.datamodel.Models
	names := make([]string, 0, len(models))

	for _, m := range models {
		if drivers.Skip(m.TableName(), tableFilter.Only, tableFilter.Except) {
			continue
		}

		names = append(names, m.TableName())
	}

	return names
}

func (p *driver) loadEnums() {
	if p.enums != nil {
		return
	}
	p.enums = map[string]drivers.Enum{}

	enums := p.datamodel.Enums
	for _, enum := range enums {
		values := make([]string, len(enum.Values))
		for i, val := range enum.Values {
			values[i] = val.Name
		}

		p.enums[enum.Name] = drivers.Enum{
			Type:   gocase.To(strcase.ToCamel(enum.Name)),
			Values: values,
		}
	}
}

func (p *driver) getEnums() []drivers.Enum {
	enums := make([]drivers.Enum, 0, len(p.enums))
	for _, e := range p.enums {
		enums = append(enums, e)
	}

	sort.Slice(enums, func(i, j int) bool {
		return enums[i].Type < enums[j].Type
	})

	return enums
}

func (d *driver) tableColumns(model Model, colFilter drivers.ColumnFilter) []drivers.Column {
	allfilter := colFilter["*"]
	filter := colFilter[model.TableName()]
	include := append(allfilter.Only, filter.Only...)
	exclude := append(allfilter.Except, filter.Except...)

	columns := make([]drivers.Column, 0, len(model.Fields))
	for _, field := range model.Fields {
		if drivers.Skip(field.Name, include, exclude) {
			continue
		}

		if field.Kind == FieldKindObject {
			continue
		}

		column := drivers.Column{
			Name:      field.Name,
			DBType:    field.Type,
			Comment:   field.Documentation,
			Nullable:  !field.IsRequired,
			Generated: field.IsGenerated,
			AutoIncr:  field.Default.AutoIncr,
		}

		if field.HasDefaultValue {
			column.Default = "DEFAULT"
		}

		// A generated column technically has a default value
		if column.Generated && column.Default == "" {
			column.Default = "GENERATED"
		}

		// A nullable column can always default to NULL
		if !field.IsRequired && column.Default == "" {
			column.Default = "NULL"
		}

		columns = append(columns, d.translateColumnType(column, field.IsList))
	}

	return columns
}

func (d *driver) translateColumnType(c drivers.Column, isArray bool) drivers.Column {
	switch isArray {
	case false: // not an array
		switch c.DBType {
		case "String":
			c.Type = "string"
		case "Boolean":
			c.Type = "bool"
		case "Int":
			c.Type = "int"
		case "BigInt":
			c.Type = "int64"
		case "Float":
			c.Type = "float64"
		case "Bytes":
			c.Type = "[]byte"
		case "Decimal":
			c.Type = "decimal.Decimal"
		case "DateTime":
			c.Type = "time.Time"
		case "Json":
			c.Type = "types.JSON[json.RawMessage]"
		default:
			if enum, ok := d.enums[c.DBType]; ok {
				c.Type = enum.Type
			} else {
				c.Type = "string"
			}
		}

	case true: // Is an array
		switch c.DBType {
		case "String":
			c.Type = "pq.StringArray"
		case "Boolean":
			c.Type = "pq.BoolArray"
		case "Int", "BigInt":
			c.Type = "pq.Int64Array"
		case "Float":
			c.Type = "pq.Float64Array"
		case "Bytes":
			c.Type = "pq.ByteaArray"
		case "Decimal":
			c.Type = "parray.Array[decimal.Decimal]"
			c.Imports = append(c.Imports, typMap["parray"]...)
			c.Imports = append(c.Imports, typMap["decimal.Decimal"]...)
		case "DateTime":
			c.Type = "parray.Array[time.Time]"
			c.Imports = append(c.Imports, typMap["parray"]...)
			c.Imports = append(c.Imports, typMap["time.Time"]...)
		case "Json":
			c.Type = "parray.Array[types.JSON[json.RawMessage]]"
			c.Imports = append(c.Imports, typMap["parray"]...)
			c.Imports = append(c.Imports, typMap["time.Time"]...)
		default:
			if enum, ok := d.enums[c.DBType]; ok {
				c.Imports = append(c.Imports, typMap["parray"]...)
				c.Type = fmt.Sprintf("parray.EnumArray[%s]", enum.Type)
			} else {
				c.Type = "pq.StringArray"
			}
		}
		c.DBType += "[]"
	}

	// fallback to other drivers?
	c.Imports = append(c.Imports, typMap[c.Type]...)
	return c
}

//nolint:gochecknoglobals
var typMap = map[string]importers.List{
	"time.Time":                   {`"time"`},
	"types.JSON[json.RawMessage]": {`"encoding/json"`, `"github.com/stephenafamo/bob/types"`},
	"decimal.Decimal":             {`"github.com/shopspring/decimal"`},
	"types.HStore":                {`"github.com/stephenafamo/bob/types"`},
	"pgeo.Point":                  {`"github.com/saulortega/pgeo"`},
	"pgeo.Line":                   {`"github.com/saulortega/pgeo"`},
	"pgeo.Lseg":                   {`"github.com/saulortega/pgeo"`},
	"pgeo.Box":                    {`"github.com/saulortega/pgeo"`},
	"pgeo.Path":                   {`"github.com/saulortega/pgeo"`},
	"pgeo.Polygon":                {`"github.com/saulortega/pgeo"`},
	"pq.ByteaArray":               {`"github.com/lib/pq"`},
	"pq.Int64Array":               {`"github.com/lib/pq"`},
	"pq.Float64Array":             {`"github.com/lib/pq"`},
	"pq.BoolArray":                {`"github.com/lib/pq"`},
	"pq.StringArray":              {`"github.com/lib/pq"`},
	"parray":                      {`"github.com/stephenafamo/bob/types/parray"`},
}

func (d *driver) getKeys(model Model, colFilter drivers.ColumnFilter) (*drivers.PrimaryKey, []drivers.Constraint, []drivers.ForeignKey) {
	var pk *drivers.PrimaryKey
	var uniques []drivers.Constraint
	var fks []drivers.ForeignKey

	tableName := model.TableName()
	filter := colFilter[tableName]
	only := filter.Only
	except := filter.Except

	// If it is a composite primary key defined on the model
	if len(model.PrimaryKey.Fields) > 0 {
		shouldSkip := false
		cols := make([]string, len(model.PrimaryKey.Fields))

		for i, f := range model.PrimaryKey.Fields {
			if drivers.Skip(f, only, except) {
				shouldSkip = true
			}
			cols[i] = f
		}

		if !shouldSkip {
			pkName := model.PrimaryKey.Name
			if pkName == "" {
				pkName = "pk_" + tableName
			}
			pk = &drivers.Constraint{
				Name:    pkName,
				Columns: cols,
			}
		}
	}

	for _, unique := range model.UniqueIndexes {
		shouldSkip := false
		cols := make([]string, len(unique.Fields))

		for i, f := range unique.Fields {
			if drivers.Skip(f, only, except) {
				shouldSkip = true
			}
			cols[i] = f
		}

		if !shouldSkip {
			keyName := unique.InternalName
			if keyName == "" {
				keyName = fmt.Sprintf("unique_%s_%s", tableName, strings.Join(cols, "_"))
			}

			uniques = append(uniques, drivers.Constraint{
				Name:    keyName,
				Columns: cols,
			})
		}
	}

	// If one of the fields has an @id attribute
	for _, field := range model.Fields {
		if drivers.Skip(field.Name, only, except) {
			continue
		}

		if field.IsID {
			pk = &drivers.Constraint{
				Name:    "pk_" + tableName,
				Columns: []string{field.Name},
			}
		}

		if field.IsUnique {
			uniques = append(uniques, drivers.Constraint{
				Name:    fmt.Sprintf("unique_%s_%s", tableName, field.Name),
				Columns: []string{field.Name},
			})
		}

		if field.Kind == FieldKindObject && len(field.RelationFromFields) > 0 {
			fks = append(fks, drivers.ForeignKey{
				Constraint: drivers.Constraint{
					Name:    field.RelationName,
					Columns: field.RelationFromFields,
				},
				ForeignTable:   d.datamodel.ModelByName(field.Type).TableName(),
				ForeignColumns: field.RelationToFields,
			})
		}
	}

	return pk, uniques, fks
}
