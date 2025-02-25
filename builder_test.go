package wazero

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/internal/testing/require"
	"github.com/tetratelabs/wazero/internal/wasm"
)

// TestNewHostModuleBuilder_Compile only covers a few scenarios to avoid duplicating tests in internal/wasm/host_test.go
func TestNewHostModuleBuilder_Compile(t *testing.T) {
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64

	uint32_uint32 := func(context.Context, uint32) uint32 {
		return 0
	}
	uint64_uint32 := func(context.Context, uint64) uint32 {
		return 0
	}

	gofunc1 := api.GoFunc(func(ctx context.Context, stack []uint64) {
		stack[0] = 0
	})
	gofunc2 := api.GoFunc(func(ctx context.Context, stack []uint64) {
		stack[0] = 0
	})

	tests := []struct {
		name     string
		input    func(Runtime) HostModuleBuilder
		expected *wasm.Module
	}{
		{
			name: "empty",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("")
			},
			expected: &wasm.Module{},
		},
		{
			name: "only name",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("env")
			},
			expected: &wasm.Module{NameSection: &wasm.NameSection{ModuleName: "env"}},
		},
		{
			name: "WithFunc",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().WithFunc(uint32_uint32).Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection:     []*wasm.Code{wasm.MustParseGoReflectFuncCode(uint32_uint32)},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}},
				},
			},
		},
		{
			name: "WithFunc WithName WithParameterNames",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").NewFunctionBuilder().
					WithFunc(uint32_uint32).
					WithName("get").WithParameterNames("x").
					Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection:     []*wasm.Code{wasm.MustParseGoReflectFuncCode(uint32_uint32)},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "get"}},
					LocalNames:    []*wasm.NameMapAssoc{{Index: 0, NameMap: wasm.NameMap{{Index: 0, Name: "x"}}}},
				},
			},
		},
		{
			name: "WithFunc overwrites existing",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().WithFunc(uint32_uint32).Export("1").
					NewFunctionBuilder().WithFunc(uint64_uint32).Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i64}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection:     []*wasm.Code{wasm.MustParseGoReflectFuncCode(uint64_uint32)},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}},
				},
			},
		},
		{
			name: "WithFunc twice",
			input: func(r Runtime) HostModuleBuilder {
				// Intentionally out of order
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().WithFunc(uint64_uint32).Export("2").
					NewFunctionBuilder().WithFunc(uint32_uint32).Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
					{Params: []api.ValueType{i64}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0, 1},
				CodeSection:     []*wasm.Code{wasm.MustParseGoReflectFuncCode(uint32_uint32), wasm.MustParseGoReflectFuncCode(uint64_uint32)},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
					{Name: "2", Type: wasm.ExternTypeFunc, Index: 1},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}, {Index: 1, Name: "2"}},
				},
			},
		},
		{
			name: "WithGoFunction",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().
					WithGoFunction(gofunc1, []api.ValueType{i32}, []api.ValueType{i32}).
					Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection: []*wasm.Code{
					{IsHostFunction: true, GoFunc: gofunc1},
				},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}},
				},
			},
		},
		{
			name: "WithGoFunction WithName WithParameterNames",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").NewFunctionBuilder().
					WithGoFunction(gofunc1, []api.ValueType{i32}, []api.ValueType{i32}).
					WithName("get").WithParameterNames("x").
					Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection: []*wasm.Code{
					{IsHostFunction: true, GoFunc: gofunc1},
				},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "get"}},
					LocalNames:    []*wasm.NameMapAssoc{{Index: 0, NameMap: wasm.NameMap{{Index: 0, Name: "x"}}}},
				},
			},
		},
		{
			name: "WithGoFunction overwrites existing",
			input: func(r Runtime) HostModuleBuilder {
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().
					WithGoFunction(gofunc1, []api.ValueType{i32}, []api.ValueType{i32}).
					Export("1").
					NewFunctionBuilder().
					WithGoFunction(gofunc2, []api.ValueType{i64}, []api.ValueType{i32}).
					Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i64}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0},
				CodeSection: []*wasm.Code{
					{IsHostFunction: true, GoFunc: gofunc2},
				},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}},
				},
			},
		},
		{
			name: "WithGoFunction twice",
			input: func(r Runtime) HostModuleBuilder {
				// Intentionally out of order
				return r.NewHostModuleBuilder("").
					NewFunctionBuilder().
					WithGoFunction(gofunc2, []api.ValueType{i64}, []api.ValueType{i32}).
					Export("2").
					NewFunctionBuilder().
					WithGoFunction(gofunc1, []api.ValueType{i32}, []api.ValueType{i32}).
					Export("1")
			},
			expected: &wasm.Module{
				TypeSection: []*wasm.FunctionType{
					{Params: []api.ValueType{i32}, Results: []api.ValueType{i32}},
					{Params: []api.ValueType{i64}, Results: []api.ValueType{i32}},
				},
				FunctionSection: []wasm.Index{0, 1},
				CodeSection: []*wasm.Code{
					{IsHostFunction: true, GoFunc: gofunc1},
					{IsHostFunction: true, GoFunc: gofunc2},
				},
				ExportSection: []*wasm.Export{
					{Name: "1", Type: wasm.ExternTypeFunc, Index: 0},
					{Name: "2", Type: wasm.ExternTypeFunc, Index: 1},
				},
				NameSection: &wasm.NameSection{
					FunctionNames: wasm.NameMap{{Index: 0, Name: "1"}, {Index: 1, Name: "2"}},
				},
			},
		},
	}

	for _, tt := range tests {
		tc := tt

		t.Run(tc.name, func(t *testing.T) {
			b := tc.input(NewRuntime(testCtx)).(*hostModuleBuilder)
			compiled, err := b.Compile(testCtx)
			require.NoError(t, err)
			m := compiled.(*compiledModule)

			requireHostModuleEquals(t, tc.expected, m.module)

			require.Equal(t, b.r.store.Engine, m.compiledEngine)

			// Built module must be instantiable by Engine.
			mod, err := b.r.InstantiateModule(testCtx, m, NewModuleConfig())
			require.NoError(t, err)

			// Closing the module shouldn't remove the compiler cache
			require.NoError(t, mod.Close(testCtx))
			require.Equal(t, uint32(1), b.r.store.Engine.CompiledModuleCount())
		})
	}
}

// TestNewHostModuleBuilder_Compile_Errors only covers a few scenarios to avoid
// duplicating tests in internal/wasm/host_test.go
func TestNewHostModuleBuilder_Compile_Errors(t *testing.T) {
	tests := []struct {
		name        string
		input       func(Runtime) HostModuleBuilder
		expectedErr string
	}{
		{
			name: "error compiling", // should fail due to missing result.
			input: func(rt Runtime) HostModuleBuilder {
				return rt.NewHostModuleBuilder("").NewFunctionBuilder().
					WithFunc(&wasm.HostFunc{
						ExportNames: []string{"fn"},
						ResultTypes: []wasm.ValueType{wasm.ValueTypeI32},
						Code:        &wasm.Code{IsHostFunction: true, Body: []byte{wasm.OpcodeEnd}},
					}).Export("fn")
			},
			expectedErr: `invalid function[0] export["fn"]: not enough results
	have ()
	want (i32)`,
		},
	}

	for _, tt := range tests {
		tc := tt

		t.Run(tc.name, func(t *testing.T) {
			_, e := tc.input(NewRuntime(testCtx)).Compile(testCtx)
			require.EqualError(t, e, tc.expectedErr)
		})
	}
}

// TestNewHostModuleBuilder_Instantiate ensures Runtime.InstantiateModule is called on success.
func TestNewHostModuleBuilder_Instantiate(t *testing.T) {
	r := NewRuntime(testCtx)
	m, err := r.NewHostModuleBuilder("env").Instantiate(testCtx, r)
	require.NoError(t, err)

	// If this was instantiated, it would be added to the store under the same name
	require.Equal(t, r.(*runtime).ns.Module("env"), m)

	// Closing the module should remove the compiler cache
	require.NoError(t, m.Close(testCtx))
	require.Zero(t, r.(*runtime).store.Engine.CompiledModuleCount())
}

// TestNewHostModuleBuilder_Instantiate_Errors ensures errors propagate from Runtime.InstantiateModule
func TestNewHostModuleBuilder_Instantiate_Errors(t *testing.T) {
	r := NewRuntime(testCtx)
	_, err := r.NewHostModuleBuilder("env").Instantiate(testCtx, r)
	require.NoError(t, err)

	_, err = r.NewHostModuleBuilder("env").Instantiate(testCtx, r)
	require.EqualError(t, err, "module[env] has already been instantiated")
}

// requireHostModuleEquals is redefined from internal/wasm/host_test.go to avoid an import cycle extracting it.
func requireHostModuleEquals(t *testing.T, expected, actual *wasm.Module) {
	// `require.Equal(t, expected, actual)` fails reflect pointers don't match, so brute compare:
	for _, tp := range expected.TypeSection {
		tp.CacheNumInUint64()
	}
	require.Equal(t, expected.TypeSection, actual.TypeSection)
	require.Equal(t, expected.ImportSection, actual.ImportSection)
	require.Equal(t, expected.FunctionSection, actual.FunctionSection)
	require.Equal(t, expected.TableSection, actual.TableSection)
	require.Equal(t, expected.MemorySection, actual.MemorySection)
	require.Equal(t, expected.GlobalSection, actual.GlobalSection)
	require.Equal(t, expected.ExportSection, actual.ExportSection)
	require.Equal(t, expected.StartSection, actual.StartSection)
	require.Equal(t, expected.ElementSection, actual.ElementSection)
	require.Equal(t, expected.DataSection, actual.DataSection)
	require.Equal(t, expected.NameSection, actual.NameSection)

	// Special case because reflect.Value can't be compared with Equals
	// TODO: This is copy/paste with /internal/wasm/host_test.go
	require.Equal(t, len(expected.CodeSection), len(actual.CodeSection))
	for i, c := range expected.CodeSection {
		actualCode := actual.CodeSection[i]
		require.True(t, actualCode.IsHostFunction)
		require.Equal(t, c.GoFunc, actualCode.GoFunc)

		// Not wasm
		require.Nil(t, actualCode.Body)
		require.Nil(t, actualCode.LocalTypes)
	}
}
