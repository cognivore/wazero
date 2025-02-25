// Package enginetest contains tests common to any wasm.Engine implementation. Defining these as top-level
// functions is less burden than copy/pasting the implementations, while still allowing test caching to operate.
//
// In simplest case, dispatch:
//
//	func TestModuleEngine_Call(t *testing.T) {
//		enginetest.RunTestModuleEngine_Call(t, NewEngine)
//	}
//
// Some tests using the Compiler Engine may need to guard as they use compiled features:
//
//	func TestModuleEngine_Call(t *testing.T) {
//		requireSupportedOSArch(t)
//		enginetest.RunTestModuleEngine_Call(t, NewEngine)
//	}
//
// Note: These tests intentionally avoid using wasm.Store as it is important to know both the dependencies and
// the capabilities at the wasm.Engine abstraction.
package enginetest

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/internal/testing/require"
	"github.com/tetratelabs/wazero/internal/u64"
	"github.com/tetratelabs/wazero/internal/wasm"
	"github.com/tetratelabs/wazero/internal/wasmruntime"
)

const (
	i32, i64 = wasm.ValueTypeI32, wasm.ValueTypeI64
)

var (
	// testCtx is an arbitrary, non-default context. Non-nil also prevents linter errors.
	testCtx = context.WithValue(context.Background(), struct{}{}, "arbitrary")
	// v_v is a nullary function type (void -> void)
	v_v          = &wasm.FunctionType{}
	func1, func2 = wasm.Index(2), wasm.Index(1)
)

type EngineTester interface {
	// IsCompiler returns true if this engine is a compiler.
	IsCompiler() bool

	NewEngine(enabledFeatures api.CoreFeatures) wasm.Engine

	ListenerFactory() experimental.FunctionListenerFactory

	// InitTables returns expected table contents ([]wasm.Reference) per table.
	InitTables(me wasm.ModuleEngine, tableIndexToLen map[wasm.Index]int,
		tableInits []wasm.TableInitEntry) [][]wasm.Reference

	// CompiledFunctionPointerValue returns the opaque compiledFunction's pointer for the `funcIndex`.
	CompiledFunctionPointerValue(tme wasm.ModuleEngine, funcIndex wasm.Index) uint64
}

func RunTestEngine_NewModuleEngine(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV1)

	t.Run("error before instantiation", func(t *testing.T) {
		_, err := e.NewModuleEngine("mymod", &wasm.Module{}, nil, nil, nil, nil)
		require.EqualError(t, err, "source module for mymod must be compiled before instantiation")
	})

	t.Run("sets module name", func(t *testing.T) {
		m := &wasm.Module{}
		err := e.CompileModule(testCtx, m)
		require.NoError(t, err)
		me, err := e.NewModuleEngine(t.Name(), m, nil, nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, t.Name(), me.Name())
	})
}

func RunTestEngine_InitializeFuncrefGlobals(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV2)

	i64 := i64
	m := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{{Params: []wasm.ValueType{i64}, Results: []wasm.ValueType{i64}}},
		FunctionSection: []wasm.Index{0, 0, 0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeEnd}, LocalTypes: []wasm.ValueType{i64}},
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeEnd}, LocalTypes: []wasm.ValueType{i64}},
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeEnd}, LocalTypes: []wasm.ValueType{i64}},
		},
	}
	m.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, m)
	require.NoError(t, err)

	// To use the function, we first need to add it to a module.
	instance := &wasm.ModuleInstance{Name: t.Name(), TypeIDs: []wasm.FunctionTypeID{0}}
	fns := instance.BuildFunctions(m, buildListeners(et.ListenerFactory(), m))
	me, err := e.NewModuleEngine(t.Name(), m, nil, fns, nil, nil)
	require.NoError(t, err)

	nullRefVal := wasm.GlobalInstanceNullFuncRefValue
	globals := []*wasm.GlobalInstance{
		{Val: 10, Type: &wasm.GlobalType{ValType: i32}},
		{Val: uint64(nullRefVal), Type: &wasm.GlobalType{ValType: wasm.ValueTypeFuncref}},
		{Val: uint64(2), Type: &wasm.GlobalType{ValType: wasm.ValueTypeFuncref}},
		{Val: uint64(1), Type: &wasm.GlobalType{ValType: wasm.ValueTypeFuncref}},
		{Val: uint64(0), Type: &wasm.GlobalType{ValType: wasm.ValueTypeFuncref}},
	}
	me.InitializeFuncrefGlobals(globals)

	// Non-funcref values must be intact.
	require.Equal(t, uint64(10), globals[0].Val)
	// The second global had wasm.GlobalInstanceNullFuncRefValue, so that value must be translated as null reference (uint64(0)).
	require.Zero(t, globals[1].Val)
	// Non GlobalInstanceNullFuncRefValue valued globals must result in having the valid compiled function's pointers.
	require.Equal(t, et.CompiledFunctionPointerValue(me, 2), globals[2].Val)
	require.Equal(t, et.CompiledFunctionPointerValue(me, 1), globals[3].Val)
	require.Equal(t, et.CompiledFunctionPointerValue(me, 0), globals[4].Val)
}

func RunTestModuleEngine_Call(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV2)

	// Define a basic function which defines two parameters and two results.
	// This is used to test results when incorrect arity is used.
	m := &wasm.Module{
		TypeSection: []*wasm.FunctionType{
			{
				Params:            []wasm.ValueType{i64, i64},
				Results:           []wasm.ValueType{i64, i64},
				ParamNumInUint64:  2,
				ResultNumInUint64: 2,
			},
		},
		FunctionSection: []wasm.Index{0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeLocalGet, 1, wasm.OpcodeEnd}},
		},
	}
	m.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, m)
	require.NoError(t, err)

	// To use the function, we first need to add it to a module.
	module := &wasm.ModuleInstance{Name: t.Name(), TypeIDs: []wasm.FunctionTypeID{0}}
	module.Functions = module.BuildFunctions(m, buildListeners(et.ListenerFactory(), m))

	// Compile the module
	me, err := e.NewModuleEngine(module.Name, m, nil, module.Functions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(module, me)

	// Ensure the base case doesn't fail: A single parameter should work as that matches the function signature.
	fn := module.Functions[0]

	ce, err := me.NewCallEngine(module.CallCtx, fn)
	require.NoError(t, err)

	results, err := ce.Call(testCtx, module.CallCtx, []uint64{1, 2})
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 2}, results)

	t.Run("errs when not enough parameters", func(t *testing.T) {
		ce, err := me.NewCallEngine(module.CallCtx, fn)
		require.NoError(t, err)

		_, err = ce.Call(testCtx, module.CallCtx, nil)
		require.EqualError(t, err, "expected 2 params, but passed 0")
	})

	t.Run("errs when too many parameters", func(t *testing.T) {
		ce, err := me.NewCallEngine(module.CallCtx, fn)
		require.NoError(t, err)

		_, err = ce.Call(testCtx, module.CallCtx, []uint64{1, 2, 3})
		require.EqualError(t, err, "expected 2 params, but passed 3")
	})
}

func RunTestEngine_NewModuleEngine_InitTable(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV1)

	t.Run("no table elements", func(t *testing.T) {
		requireNewModuleEngine_emptyTable(t, e, et)
	})
	t.Run("multi-table", func(t *testing.T) {
		requireNewModuleEngine_multiTable(t, e, et)
	})

	t.Run("imported function", func(t *testing.T) {
		requireNewModuleEngine_tableWithImportedFunction(t, e, et)
	})

	t.Run("mixed functions", func(t *testing.T) {
		requireNewModuleEngine_tableWithMixedFunctions(t, e, et)
	})
}

func requireNewModuleEngine_emptyTable(t *testing.T, e wasm.Engine, et EngineTester) (me wasm.ModuleEngine, module *wasm.ModuleInstance) {
	tables := []*wasm.TableInstance{{Min: 2, References: make([]wasm.Reference, 2)}}

	// define a module with a function, but it isn't in the table.
	m := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{v_v},
		FunctionSection: []wasm.Index{0},
		CodeSection:     []*wasm.Code{{Body: []byte{wasm.OpcodeEnd}}},
		ID:              wasm.ModuleID{0},
	}
	m.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, m)
	require.NoError(t, err)

	module = &wasm.ModuleInstance{Name: t.Name(), Tables: tables, TypeIDs: []wasm.FunctionTypeID{0}}
	fns := module.BuildFunctions(m, buildListeners(et.ListenerFactory(), m))

	// Instantiate the module, which has nothing but an empty table.
	me, err = e.NewModuleEngine(t.Name(), m, nil, fns, tables, nil)
	require.NoError(t, err)

	// Since there are no elements to initialize, we expect the table to be nil.
	require.Equal(t, tables[0].References, make([]wasm.Reference, 2))

	return
}

// requireNewModuleEngine_multiTable ensures WebAssembly 2.0 multi-table feature works.
func requireNewModuleEngine_multiTable(t *testing.T, e wasm.Engine, et EngineTester) (me wasm.ModuleEngine, module *wasm.ModuleInstance) {
	tables := []*wasm.TableInstance{
		{Min: 2, References: make([]wasm.Reference, 2)},
		{Min: 10, References: make([]wasm.Reference, 10)},
	}

	m := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{v_v},
		FunctionSection: []wasm.Index{0, 0, 0, 0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
		},
		ID: wasm.ModuleID{1},
	}
	m.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, m)
	require.NoError(t, err)

	module = &wasm.ModuleInstance{Name: t.Name(), Tables: tables, TypeIDs: []wasm.FunctionTypeID{0}}
	fns := module.BuildFunctions(m, buildListeners(et.ListenerFactory(), m))

	tableInits := []wasm.TableInitEntry{
		{TableIndex: 0, Offset: 0, FunctionIndexes: []*wasm.Index{&func1}},
		{TableIndex: 1, Offset: 5, FunctionIndexes: []*wasm.Index{&func2}},
	}

	// Instantiate the module whose table points to its own functions.
	me, err = e.NewModuleEngine(t.Name(), m, nil, fns, tables, tableInits)
	require.NoError(t, err)

	// The functions mapped to the table are defined in the same moduleEngine
	expectedTables := et.InitTables(me, map[wasm.Index]int{0: 2, 1: 10}, tableInits)
	for idx, table := range tables {
		require.Equal(t, expectedTables[idx], table.References)
	}

	return
}

func requireNewModuleEngine_tableWithImportedFunction(t *testing.T, e wasm.Engine, et EngineTester) (importingMe wasm.ModuleEngine, importing *wasm.ModuleInstance) {
	tables := []*wasm.TableInstance{{Min: 2, References: make([]wasm.Reference, 2)}}

	importedModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{v_v},
		FunctionSection: []wasm.Index{0, 0, 0, 0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
		},
		ID: wasm.ModuleID{2},
	}
	importedModule.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, importedModule)
	require.NoError(t, err)

	imported := &wasm.ModuleInstance{Name: t.Name(), Tables: tables, TypeIDs: []wasm.FunctionTypeID{0}}
	importedFunctions := imported.BuildFunctions(importedModule, buildListeners(et.ListenerFactory(), importedModule))

	// Imported functions are compiled before the importing module is instantiated.
	importedMe, err := e.NewModuleEngine(t.Name(), importedModule, nil, importedFunctions, nil, nil)
	require.NoError(t, err)
	imported.Engine = importedMe

	// Instantiate the importing module, which is whose table is initialized.
	importingModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{},
		FunctionSection: []wasm.Index{},
		CodeSection:     []*wasm.Code{},
		ID:              wasm.ModuleID{3},
	}
	importingModule.BuildFunctionDefinitions()
	err = e.CompileModule(testCtx, importingModule)
	require.NoError(t, err)

	tableInits := []wasm.TableInitEntry{
		{TableIndex: 0, Offset: 0, FunctionIndexes: []*wasm.Index{&func1}},
	}

	importing = &wasm.ModuleInstance{Name: t.Name(), Tables: tables, TypeIDs: []wasm.FunctionTypeID{0}}
	fns := importing.BuildFunctions(importingModule, buildListeners(et.ListenerFactory(), importingModule))

	importingMe, err = e.NewModuleEngine(t.Name(), importingModule, importedFunctions, fns, tables, tableInits)
	require.NoError(t, err)

	// A moduleEngine's compiled function slice includes its imports, so the offsets is absolute.
	expectedTables := et.InitTables(importingMe, map[wasm.Index]int{0: 2}, tableInits)
	for idx, table := range tables {
		require.Equal(t, expectedTables[idx], table.References)
	}
	return
}

func requireNewModuleEngine_tableWithMixedFunctions(t *testing.T, e wasm.Engine, et EngineTester) (importingMe wasm.ModuleEngine, importing *wasm.ModuleInstance) {
	tables := []*wasm.TableInstance{{Min: 2, References: make([]wasm.Reference, 2)}}

	importedModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{v_v},
		FunctionSection: []wasm.Index{0, 0, 0, 0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeEnd}}, {Body: []byte{wasm.OpcodeEnd}}, {Body: []byte{wasm.OpcodeEnd}}, {Body: []byte{wasm.OpcodeEnd}},
		},
		ID: wasm.ModuleID{4},
	}
	importedModule.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, importedModule)
	require.NoError(t, err)
	imported := &wasm.ModuleInstance{Name: t.Name(), TypeIDs: []wasm.FunctionTypeID{0}}
	importedFunctions := imported.BuildFunctions(importedModule, buildListeners(et.ListenerFactory(), importedModule))

	// Imported functions are compiled before the importing module is instantiated.
	importedMe, err := e.NewModuleEngine(t.Name(), importedModule, nil, importedFunctions, nil, nil)
	require.NoError(t, err)
	imported.Engine = importedMe

	importingModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{v_v},
		FunctionSection: []wasm.Index{0, 0, 0, 0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
			{Body: []byte{wasm.OpcodeEnd}},
		},
		ID: wasm.ModuleID{5},
	}
	importingModule.BuildFunctionDefinitions()
	err = e.CompileModule(testCtx, importingModule)
	require.NoError(t, err)

	importing = &wasm.ModuleInstance{Name: t.Name(), Tables: tables, TypeIDs: []wasm.FunctionTypeID{0}}
	fns := importing.BuildFunctions(importingModule, buildListeners(et.ListenerFactory(), importingModule))

	tableInits := []wasm.TableInitEntry{
		{TableIndex: 0, Offset: 0, FunctionIndexes: []*wasm.Index{&func1, &func2}},
	}

	// Instantiate the importing module, which is whose table is initialized.
	importingMe, err = e.NewModuleEngine(t.Name(), importingModule, importedFunctions, fns, tables, tableInits)
	require.NoError(t, err)

	// A moduleEngine's compiled function slice includes its imports, so the offsets are absolute.
	expectedTables := et.InitTables(importingMe, map[wasm.Index]int{0: 2}, tableInits)
	for idx, table := range tables {
		require.Equal(t, expectedTables[idx], table.References)
	}

	return
}

func RunTestModuleEngine_LookupFunction(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV1)

	t.Run("no table elements", func(t *testing.T) {
		me, m := requireNewModuleEngine_emptyTable(t, e, et)

		_, err := me.LookupFunction(m.Tables[0], m.TypeIDs[0], 0 /* out of range */)
		require.Equal(t, wasmruntime.ErrRuntimeInvalidTableAccess, err)
	})

	t.Run("multi-table", func(t *testing.T) {
		me, m := requireNewModuleEngine_multiTable(t, e, et)

		// table[0][0] should point to func1
		idx, err := me.LookupFunction(m.Tables[0], m.TypeIDs[0], 0)
		require.NoError(t, err)
		require.Equal(t, func1, idx)

		// table[1][5] should point to func2
		idx, err = me.LookupFunction(m.Tables[1], m.TypeIDs[0], 5)
		require.NoError(t, err)
		require.Equal(t, func2, idx)
	})

	t.Run("imported function", func(t *testing.T) {
		me, m := requireNewModuleEngine_tableWithImportedFunction(t, e, et)

		// table[0][0] should point to func1
		idx, err := me.LookupFunction(m.Tables[0], m.TypeIDs[0], 0)
		require.NoError(t, err)
		require.Equal(t, func1, idx)
	})

	t.Run("mixed functions", func(t *testing.T) {
		me, m := requireNewModuleEngine_tableWithMixedFunctions(t, e, et)

		// table[0][0] should point to func1
		idx, err := me.LookupFunction(m.Tables[0], m.TypeIDs[0], 0)
		require.NoError(t, err)
		require.Equal(t, func1, idx)

		// table[0][1] should point to func2
		idx, err = me.LookupFunction(m.Tables[0], m.TypeIDs[0], 1)
		require.NoError(t, err)
		require.Equal(t, func2, idx)
	})
}

func runTestModuleEngine_Call_HostFn_Mem(t *testing.T, et EngineTester, readMem *wasm.Code) {
	e := et.NewEngine(api.CoreFeaturesV1)
	_, importing, done := setupCallMemTests(t, e, readMem, et.ListenerFactory())
	defer done()

	importingMemoryVal := uint64(6)
	importing.Memory = &wasm.MemoryInstance{Buffer: u64.LeBytes(importingMemoryVal), Min: 1, Cap: 1, Max: 1}

	tests := []struct {
		name     string
		fn       *wasm.FunctionInstance
		expected uint64
	}{
		{
			name:     callImportReadMemName,
			fn:       importing.Exports[callImportReadMemName].Function,
			expected: importingMemoryVal,
		},
		{
			name:     callImportCallReadMemName,
			fn:       importing.Exports[callImportCallReadMemName].Function,
			expected: importingMemoryVal,
		},
	}
	for _, tt := range tests {
		tc := tt

		t.Run(tc.name, func(t *testing.T) {
			ce, err := tc.fn.Module.Engine.NewCallEngine(tc.fn.Module.CallCtx, tc.fn)
			require.NoError(t, err)

			results, err := ce.Call(testCtx, importing.CallCtx, nil)
			require.NoError(t, err)
			require.Equal(t, tc.expected, results[0])
		})
	}
}

func RunTestModuleEngine_Call_HostFn(t *testing.T, et EngineTester) {
	t.Run("wasm", func(t *testing.T) {
		runTestModuleEngine_Call_HostFn(t, et, hostDivByWasm)
		runTestModuleEngine_Call_HostFn_Mem(t, et, hostReadMemWasm)
	})
	t.Run("go", func(t *testing.T) {
		runTestModuleEngine_Call_HostFn(t, et, hostDivByGo)
		runTestModuleEngine_Call_HostFn_Mem(t, et, hostReadMemGo)
	})
}

func runTestModuleEngine_Call_HostFn(t *testing.T, et EngineTester, hostDivBy *wasm.Code) {
	e := et.NewEngine(api.CoreFeaturesV1)

	_, imported, importing, done := setupCallTests(t, e, hostDivBy, et.ListenerFactory())
	defer done()

	// Ensure the base case doesn't fail: A single parameter should work as that matches the function signature.
	tests := []struct {
		name   string
		module *wasm.CallContext
		fn     *wasm.FunctionInstance
	}{
		{
			name:   divByWasmName,
			module: imported.CallCtx,
			fn:     imported.Exports[divByWasmName].Function,
		},
		{
			name:   callDivByGoName,
			module: imported.CallCtx,
			fn:     imported.Exports[callDivByGoName].Function,
		},
		{
			name:   callImportCallDivByGoName,
			module: importing.CallCtx,
			fn:     importing.Exports[callImportCallDivByGoName].Function,
		},
	}
	for _, tt := range tests {
		tc := tt

		t.Run(tc.name, func(t *testing.T) {
			m := tc.module
			f := tc.fn

			ce, err := f.Module.Engine.NewCallEngine(m, f)
			require.NoError(t, err)

			results, err := ce.Call(testCtx, m, []uint64{1})
			require.NoError(t, err)
			require.Equal(t, uint64(1), results[0])

			results2, err := ce.Call(testCtx, m, []uint64{1})
			require.NoError(t, err)
			require.Equal(t, results, results2)

			// Ensure the result slices are unique
			results[0] = 255
			require.Equal(t, uint64(1), results2[0])
		})
	}
}

func RunTestModuleEngine_Call_Errors(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV1)

	_, imported, importing, done := setupCallTests(t, e, hostDivByGo, et.ListenerFactory())
	defer done()

	tests := []struct {
		name        string
		module      *wasm.CallContext
		fn          *wasm.FunctionInstance
		input       []uint64
		expectedErr string
	}{
		{
			name:        "wasm function not enough parameters",
			input:       []uint64{},
			module:      imported.CallCtx,
			fn:          imported.Exports[divByWasmName].Function,
			expectedErr: `expected 1 params, but passed 0`,
		},
		{
			name:        "wasm function too many parameters",
			input:       []uint64{1, 2},
			module:      imported.CallCtx,
			fn:          imported.Exports[divByWasmName].Function,
			expectedErr: `expected 1 params, but passed 2`,
		},
		{
			name:   "wasm function panics with wasmruntime.Error",
			input:  []uint64{0},
			module: imported.CallCtx,
			fn:     imported.Exports[divByWasmName].Function,
			expectedErr: `wasm error: integer divide by zero
wasm stack trace:
	imported.div_by.wasm(i32) i32`,
		},
		{
			name:   "wasm calls host function that panics",
			input:  []uint64{math.MaxUint32},
			module: imported.CallCtx,
			fn:     imported.Exports[callDivByGoName].Function,
			expectedErr: `host-function panic (recovered by wazero)
wasm stack trace:
	host.div_by.go(i32) i32
	imported.call->div_by.go(i32) i32`,
		},
		{
			name:   "wasm calls imported wasm that calls host function panics with runtime.Error",
			input:  []uint64{0},
			module: importing.CallCtx,
			fn:     importing.Exports[callImportCallDivByGoName].Function,
			expectedErr: `runtime error: integer divide by zero (recovered by wazero)
wasm stack trace:
	host.div_by.go(i32) i32
	imported.call->div_by.go(i32) i32
	importing.call_import->call->div_by.go(i32) i32`,
		},
		{
			name:   "wasm calls imported wasm that calls host function that panics",
			input:  []uint64{math.MaxUint32},
			module: importing.CallCtx,
			fn:     importing.Exports[callImportCallDivByGoName].Function,
			expectedErr: `host-function panic (recovered by wazero)
wasm stack trace:
	host.div_by.go(i32) i32
	imported.call->div_by.go(i32) i32
	importing.call_import->call->div_by.go(i32) i32`,
		},
		{
			name:   "wasm calls imported wasm calls host function panics with runtime.Error",
			input:  []uint64{0},
			module: importing.CallCtx,
			fn:     importing.Exports[callImportCallDivByGoName].Function,
			expectedErr: `runtime error: integer divide by zero (recovered by wazero)
wasm stack trace:
	host.div_by.go(i32) i32
	imported.call->div_by.go(i32) i32
	importing.call_import->call->div_by.go(i32) i32`,
		},
	}
	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			m := tc.module
			f := tc.fn

			ce, err := f.Module.Engine.NewCallEngine(m, f)
			require.NoError(t, err)

			_, err = ce.Call(testCtx, m, tc.input)
			require.EqualError(t, err, tc.expectedErr)

			// Ensure the module still works
			results, err := ce.Call(testCtx, m, []uint64{1})
			require.NoError(t, err)
			require.Equal(t, uint64(1), results[0])
		})
	}
}

// RunTestModuleEngine_Memory shows that the byte slice returned from api.Memory Read is not a copy, rather a re-slice
// of the underlying memory. This allows both host and Wasm to see each other's writes, unless one side changes the
// capacity of the slice.
//
// Known cases that change the slice capacity:
// * Host code calls append on a byte slice returned by api.Memory Read
// * Wasm code calls wasm.OpcodeMemoryGrowName and this changes the capacity (by default, it will).
func RunTestModuleEngine_Memory(t *testing.T, et EngineTester) {
	e := et.NewEngine(api.CoreFeaturesV2)

	wasmPhrase := "Well, that'll be the day when you say goodbye."
	wasmPhraseSize := uint32(len(wasmPhrase))

	// Define a basic function which defines one parameter. This is used to test results when incorrect arity is used.
	one := uint32(1)
	m := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{{Params: []api.ValueType{api.ValueTypeI32}, ParamNumInUint64: 1}, v_v},
		FunctionSection: []wasm.Index{0, 1},
		MemorySection:   &wasm.Memory{Min: 1, Cap: 1, Max: 2},
		DataSection: []*wasm.DataSegment{
			{
				OffsetExpression: nil, // passive
				Init:             []byte(wasmPhrase),
			},
		},
		DataCountSection: &one,
		CodeSection: []*wasm.Code{
			{Body: []byte{ // "grow"
				wasm.OpcodeLocalGet, 0, // how many pages to grow (param)
				wasm.OpcodeMemoryGrow, 0, // memory index zero
				wasm.OpcodeDrop, // drop the previous page count (or -1 if grow failed)
				wasm.OpcodeEnd,
			}},
			{Body: []byte{ // "init"
				wasm.OpcodeI32Const, 0, // target offset
				wasm.OpcodeI32Const, 0, // source offset
				wasm.OpcodeI32Const, byte(wasmPhraseSize), // len
				wasm.OpcodeMiscPrefix, wasm.OpcodeMiscMemoryInit, 0, 0, // segment 0, memory 0
				wasm.OpcodeEnd,
			}},
		},
		ExportSection: []*wasm.Export{
			{Name: "grow", Type: wasm.ExternTypeFunc, Index: 0},
			{Name: "init", Type: wasm.ExternTypeFunc, Index: 1},
		},
	}
	m.BuildFunctionDefinitions()

	err := e.CompileModule(testCtx, m)
	require.NoError(t, err)

	// Assign memory to the module instance
	module := &wasm.ModuleInstance{
		Name:          t.Name(),
		Memory:        wasm.NewMemoryInstance(m.MemorySection),
		DataInstances: []wasm.DataInstance{m.DataSection[0].Init},
		TypeIDs:       []wasm.FunctionTypeID{0, 1},
	}
	var memory api.Memory = module.Memory

	// To use functions, we need to instantiate them (associate them with a ModuleInstance).
	module.Functions = module.BuildFunctions(m, buildListeners(et.ListenerFactory(), m))
	module.BuildExports(m.ExportSection)
	grow, init := module.Functions[0], module.Functions[1]

	// Compile the module
	me, err := e.NewModuleEngine(module.Name, m, nil, module.Functions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(module, me)

	buf, ok := memory.Read(testCtx, 0, wasmPhraseSize)
	require.True(t, ok)
	require.Equal(t, make([]byte, wasmPhraseSize), buf)

	// Initialize the memory using Wasm. This copies the test phrase.
	initCallEngine, err := me.NewCallEngine(module.CallCtx, init)
	require.NoError(t, err)
	_, err = initCallEngine.Call(testCtx, module.CallCtx, nil)
	require.NoError(t, err)

	// We expect the same []byte read earlier to now include the phrase in wasm.
	require.Equal(t, wasmPhrase, string(buf))

	hostPhrase := "Goodbye, cruel world. I'm off to join the circus." // Intentionally slightly longer.
	hostPhraseSize := uint32(len(hostPhrase))

	// Copy over the buffer, which should stop at the current length.
	copy(buf, hostPhrase)
	require.Equal(t, "Goodbye, cruel world. I'm off to join the circ", string(buf))

	// The underlying memory should be updated. This proves that Memory.Read returns a re-slice, not a copy, and that
	// programs can rely on this (for example, to update shared state in Wasm and view that in Go and visa versa).
	buf2, ok := memory.Read(testCtx, 0, wasmPhraseSize)
	require.True(t, ok)
	require.Equal(t, buf, buf2)

	// Now, append to the buffer we got from Wasm. As this changes capacity, it should result in a new byte slice.
	buf = append(buf, 'u', 's', '.')
	require.Equal(t, hostPhrase, string(buf))

	// To prove the above, we re-read the memory and should not see the appended bytes (rather zeros instead).
	buf2, ok = memory.Read(testCtx, 0, hostPhraseSize)
	require.True(t, ok)
	hostPhraseTruncated := "Goodbye, cruel world. I'm off to join the circ" + string([]byte{0, 0, 0})
	require.Equal(t, hostPhraseTruncated, string(buf2))

	// Now, we need to prove the other direction, that when Wasm changes the capacity, the host's buffer is unaffected.
	growCallEngine, err := me.NewCallEngine(module.CallCtx, grow)
	require.NoError(t, err)
	_, err = growCallEngine.Call(testCtx, module.CallCtx, []uint64{1})
	require.NoError(t, err)

	// The host buffer should still contain the same bytes as before grow
	require.Equal(t, hostPhraseTruncated, string(buf2))

	// Re-initialize the memory in wasm, which overwrites the region.
	initCallEngine2, err := me.NewCallEngine(module.CallCtx, init)
	require.NoError(t, err)
	_, err = initCallEngine2.Call(testCtx, module.CallCtx, nil)
	require.NoError(t, err)

	// The host was not affected because it is a different slice due to "memory.grow" affecting the underlying memory.
	require.Equal(t, hostPhraseTruncated, string(buf2))
}

const (
	divByWasmName             = "div_by.wasm"
	divByGoName               = "div_by.go"
	callDivByGoName           = "call->" + divByGoName
	callImportCallDivByGoName = "call_import->" + callDivByGoName
)

func divByGo(d uint32) uint32 {
	if d == math.MaxUint32 {
		panic(errors.New("host-function panic"))
	}
	return 1 / d // go panics if d == 0
}

var hostDivByGo = wasm.MustParseGoReflectFuncCode(divByGo)

// (func (export "div_by.wasm") (param i32) (result i32) (i32.div_u (i32.const 1) (local.get 0)))
var (
	divByWasm     = []byte{wasm.OpcodeI32Const, 1, wasm.OpcodeLocalGet, 0, wasm.OpcodeI32DivU, wasm.OpcodeEnd}
	hostDivByWasm = &wasm.Code{IsHostFunction: true, Body: divByWasm}
)

const (
	readMemName               = "read_mem"
	callReadMemName           = "call->read_mem"
	callImportReadMemName     = "call_import->read_mem"
	callImportCallReadMemName = "call_import->call->read_mem"
)

func readMemGo(ctx context.Context, m api.Module) uint64 {
	ret, ok := m.Memory().ReadUint64Le(ctx, 0)
	if !ok {
		panic("couldn't read memory")
	}
	return ret
}

var hostReadMemGo = wasm.MustParseGoReflectFuncCode(readMemGo)

// (func (export "wasm_read_mem") (result i64) i32.const 0 i64.load)
var (
	readMemWasm     = []byte{wasm.OpcodeI32Const, 0, wasm.OpcodeI64Load, 0x3, 0x0, wasm.OpcodeEnd}
	hostReadMemWasm = &wasm.Code{IsHostFunction: true, Body: readMemWasm}
)

func setupCallTests(t *testing.T, e wasm.Engine, divBy *wasm.Code, fnlf experimental.FunctionListenerFactory) (*wasm.ModuleInstance, *wasm.ModuleInstance, *wasm.ModuleInstance, func()) {
	ft := &wasm.FunctionType{Params: []wasm.ValueType{i32}, Results: []wasm.ValueType{i32}, ParamNumInUint64: 1, ResultNumInUint64: 1}

	divByName := divByWasmName
	if divBy.GoFunc != nil {
		divByName = divByGoName
	}
	hostModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{ft},
		FunctionSection: []wasm.Index{0},
		CodeSection:     []*wasm.Code{divBy},
		ExportSection:   []*wasm.Export{{Name: divByGoName, Type: wasm.ExternTypeFunc, Index: 0}},
		NameSection: &wasm.NameSection{
			ModuleName:    "host",
			FunctionNames: wasm.NameMap{{Index: wasm.Index(0), Name: divByName}},
		},
		ID: wasm.ModuleID{0},
	}
	hostModule.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, hostModule)
	require.NoError(t, err)
	host := &wasm.ModuleInstance{Name: hostModule.NameSection.ModuleName, TypeIDs: []wasm.FunctionTypeID{0}}
	host.Functions = host.BuildFunctions(hostModule, buildListeners(fnlf, hostModule))
	host.BuildExports(hostModule.ExportSection)
	hostFn := host.Exports[divByGoName].Function

	hostME, err := e.NewModuleEngine(host.Name, hostModule, nil, host.Functions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(host, hostME)

	importedModule := &wasm.Module{
		ImportSection:   []*wasm.Import{{}},
		TypeSection:     []*wasm.FunctionType{ft},
		FunctionSection: []wasm.Index{0, 0},
		CodeSection: []*wasm.Code{
			{Body: divByWasm},
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeCall, byte(0), // Calling imported host function ^.
				wasm.OpcodeEnd}},
		},
		ExportSection: []*wasm.Export{
			{Name: divByWasmName, Type: wasm.ExternTypeFunc, Index: 1},
			{Name: callDivByGoName, Type: wasm.ExternTypeFunc, Index: 2},
		},
		NameSection: &wasm.NameSection{
			ModuleName: "imported",
			FunctionNames: wasm.NameMap{
				{Index: wasm.Index(1), Name: divByWasmName},
				{Index: wasm.Index(2), Name: callDivByGoName},
			},
		},
		ID: wasm.ModuleID{1},
	}
	importedModule.BuildFunctionDefinitions()
	err = e.CompileModule(testCtx, importedModule)
	require.NoError(t, err)

	imported := &wasm.ModuleInstance{Name: importedModule.NameSection.ModuleName, TypeIDs: []wasm.FunctionTypeID{0}}
	importedFunctions := imported.BuildFunctions(importedModule, buildListeners(fnlf, importedModule))
	imported.Functions = append([]*wasm.FunctionInstance{hostFn}, importedFunctions...)
	imported.BuildExports(importedModule.ExportSection)
	callHostFn := imported.Exports[callDivByGoName].Function

	// Compile the imported module
	importedMe, err := e.NewModuleEngine(imported.Name, importedModule, []*wasm.FunctionInstance{hostFn}, importedFunctions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(imported, importedMe)

	// To test stack traces, call the same function from another module
	importingModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{ft},
		ImportSection:   []*wasm.Import{{}},
		FunctionSection: []wasm.Index{0},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeLocalGet, 0, wasm.OpcodeCall, 0 /* only one imported function */, wasm.OpcodeEnd}},
		},
		ExportSection: []*wasm.Export{
			{Name: callImportCallDivByGoName, Type: wasm.ExternTypeFunc, Index: 1},
		},
		NameSection: &wasm.NameSection{
			ModuleName:    "importing",
			FunctionNames: wasm.NameMap{{Index: wasm.Index(1), Name: callImportCallDivByGoName}},
		},
		ID: wasm.ModuleID{2},
	}
	importingModule.BuildFunctionDefinitions()
	err = e.CompileModule(testCtx, importingModule)
	require.NoError(t, err)

	// Add the exported function.
	importing := &wasm.ModuleInstance{Name: importingModule.NameSection.ModuleName, TypeIDs: []wasm.FunctionTypeID{0}}
	importingFunctions := importing.BuildFunctions(importingModule, buildListeners(fnlf, importingModule))
	importing.Functions = append([]*wasm.FunctionInstance{callHostFn}, importingFunctions...)
	importing.BuildExports(importingModule.ExportSection)

	// Compile the importing module
	importingMe, err := e.NewModuleEngine(importing.Name, importingModule, []*wasm.FunctionInstance{callHostFn}, importingFunctions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(importing, importingMe)

	return host, imported, importing, func() {
		e.DeleteCompiledModule(hostModule)
		e.DeleteCompiledModule(importedModule)
		e.DeleteCompiledModule(importingModule)
	}
}

func setupCallMemTests(t *testing.T, e wasm.Engine, readMem *wasm.Code, fnlf experimental.FunctionListenerFactory) (*wasm.ModuleInstance, *wasm.ModuleInstance, func()) {
	ft := &wasm.FunctionType{Results: []wasm.ValueType{i64}, ResultNumInUint64: 1}

	callReadMem := &wasm.Code{ // shows indirect calls still use the same memory
		IsHostFunction: true,
		Body: []byte{
			wasm.OpcodeCall, 1,
			// On the return from the another host function,
			// we should still be able to access the memory.
			wasm.OpcodeI32Const, 0,
			wasm.OpcodeI32Load, 0x2, 0x0,
			wasm.OpcodeEnd,
		},
	}
	hostModule := &wasm.Module{
		TypeSection:     []*wasm.FunctionType{ft},
		FunctionSection: []wasm.Index{0, 0},
		CodeSection:     []*wasm.Code{callReadMem, readMem},
		ExportSection: []*wasm.Export{
			{Name: callReadMemName, Type: wasm.ExternTypeFunc, Index: 0},
			{Name: readMemName, Type: wasm.ExternTypeFunc, Index: 1},
		},
		NameSection: &wasm.NameSection{
			ModuleName:    "host",
			FunctionNames: wasm.NameMap{{Index: 0, Name: readMemName}, {Index: 1, Name: callReadMemName}},
		},
		ID: wasm.ModuleID{0},
	}
	hostModule.BuildFunctionDefinitions()
	err := e.CompileModule(testCtx, hostModule)
	require.NoError(t, err)
	host := &wasm.ModuleInstance{Name: hostModule.NameSection.ModuleName, TypeIDs: []wasm.FunctionTypeID{0}}
	host.Functions = host.BuildFunctions(hostModule, buildListeners(fnlf, hostModule))
	host.BuildExports(hostModule.ExportSection)
	readMemFn := host.Exports[readMemName].Function
	callReadMemFn := host.Exports[callReadMemName].Function

	hostME, err := e.NewModuleEngine(host.Name, hostModule, nil, host.Functions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(host, hostME)

	importingModule := &wasm.Module{
		TypeSection: []*wasm.FunctionType{ft},
		ImportSection: []*wasm.Import{
			// Placeholder for two import functions from `importedModule`.
			{Type: wasm.ExternTypeFunc, DescFunc: 0},
			{Type: wasm.ExternTypeFunc, DescFunc: 0},
		},
		FunctionSection: []wasm.Index{0, 0},
		ExportSection: []*wasm.Export{
			{Name: callImportReadMemName, Type: wasm.ExternTypeFunc, Index: 2},
			{Name: callImportCallReadMemName, Type: wasm.ExternTypeFunc, Index: 3},
		},
		CodeSection: []*wasm.Code{
			{Body: []byte{wasm.OpcodeCall, 0, wasm.OpcodeEnd}}, // Calling the index 0 = callReadMemFn.
			{Body: []byte{wasm.OpcodeCall, 1, wasm.OpcodeEnd}}, // Calling the index 1 = readMemFn.
		},
		NameSection: &wasm.NameSection{
			ModuleName: "importing",
			FunctionNames: wasm.NameMap{
				{Index: 2, Name: callImportReadMemName},
				{Index: 3, Name: callImportCallReadMemName},
			},
		},
		// Indicates that this module has a memory so that compilers are able to assembe memory-related initialization.
		MemorySection: &wasm.Memory{Min: 1},
		ID:            wasm.ModuleID{1},
	}
	importingModule.BuildFunctionDefinitions()
	err = e.CompileModule(testCtx, importingModule)
	require.NoError(t, err)

	// Add the exported function.
	importing := &wasm.ModuleInstance{Name: importingModule.NameSection.ModuleName, TypeIDs: []wasm.FunctionTypeID{0}}
	importingFunctions := importing.BuildFunctions(importingModule, buildListeners(fnlf, importingModule))
	// Note: adds imported functions readMemFn and callReadMemFn at index 0 and 1.
	importing.Functions = append([]*wasm.FunctionInstance{callReadMemFn, readMemFn}, importingFunctions...)
	importing.BuildExports(importingModule.ExportSection)

	// Compile the importing module
	importingMe, err := e.NewModuleEngine(importing.Name, importingModule, []*wasm.FunctionInstance{readMemFn, callReadMemFn}, importingFunctions, nil, nil)
	require.NoError(t, err)
	linkModuleToEngine(importing, importingMe)

	return host, importing, func() {
		e.DeleteCompiledModule(hostModule)
		e.DeleteCompiledModule(importingModule)
	}
}

// linkModuleToEngine assigns fields that wasm.Store would on instantiation. These includes fields both interpreter and
// Compiler needs as well as fields only needed by Compiler.
//
// Note: This sets fields that are not needed in the interpreter, but are required by code compiled by Compiler. If a new
// test here passes in the interpreter and segmentation faults in Compiler, check for a new field offset or a change in Compiler
// (e.g. compiler.TestVerifyOffsetValue). It is possible for all other tests to pass as that field is implicitly set by
// wasm.Store: store isn't used here for unit test precision.
func linkModuleToEngine(module *wasm.ModuleInstance, me wasm.ModuleEngine) {
	module.Engine = me // for Compiler, links the module to the module-engine compiled from it (moduleInstanceEngineOffset).
	// callEngineModuleContextModuleInstanceAddressOffset
	module.CallCtx = wasm.NewCallContext(nil, module, nil)
}

func buildListeners(factory experimental.FunctionListenerFactory, m *wasm.Module) []experimental.FunctionListener {
	if factory == nil || len(m.FunctionSection) == 0 {
		return nil
	}
	listeners := make([]experimental.FunctionListener, len(m.FunctionSection))
	importCount := m.ImportFuncCount()
	for i := 0; i < len(listeners); i++ {
		listeners[i] = factory.NewListener(m.FunctionDefinitionSection[uint32(i)+importCount])
	}
	return listeners
}
