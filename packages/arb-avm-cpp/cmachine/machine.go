/*
 * Copyright 2019-2020, Offchain Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmachine

/*
#cgo CFLAGS: -I.
#cgo LDFLAGS: -L. -L../build/rocksdb -lcavm -lavm -ldata_storage -lavm_values -lstdc++ -lm -lrocksdb -lsecp256k1 -lff -lgmp -lkeccak -ldl
#include "../cavm/cmachine.h"
#include "../cavm/carbstorage.h"
#include <stdio.h>
#include <stdlib.h>
*/
import "C"

import (
	"runtime"
	"unsafe"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"

	"github.com/offchainlabs/arbitrum/packages/arb-util/common"
	"github.com/offchainlabs/arbitrum/packages/arb-util/inbox"
	"github.com/offchainlabs/arbitrum/packages/arb-util/machine"
	"github.com/offchainlabs/arbitrum/packages/arb-util/protocol"
	"github.com/offchainlabs/arbitrum/packages/arb-util/value"
)

var logger = log.With().Caller().Str("component", "cmachine").Logger()

type Machine struct {
	c unsafe.Pointer
}

func New(codeFile string) (*Machine, error) {
	cFilename := C.CString(codeFile)
	defer C.free(unsafe.Pointer(cFilename))
	cMachine := C.machineCreate(cFilename)
	if cMachine == nil {
		return nil, errors.Errorf("error creating machine from file %s", codeFile)
	}

	ret := &Machine{cMachine}

	runtime.SetFinalizer(ret, cdestroyVM)

	return ret, nil
}

func cdestroyVM(cMachine *Machine) {
	C.machineDestroy(cMachine.c)
}

func (m *Machine) Hash() (ret common.Hash) {
	C.machineHash(m.c, unsafe.Pointer(&ret[0]))
	return
}

func (m *Machine) Clone() machine.Machine {
	cMachine := C.machineClone(m.c)
	ret := &Machine{cMachine}
	runtime.SetFinalizer(ret, cdestroyVM)
	return ret
}

func (m *Machine) CurrentStatus() machine.Status {
	cStatus := C.machineCurrentStatus(m.c)
	switch cStatus {
	case C.STATUS_EXTENSIVE:
		return machine.Extensive
	case C.STATUS_ERROR_STOP:
		return machine.ErrorStop

	case C.STATUS_HALT:
		return machine.Halt
	default:
		panic("Unknown status")
	}
}

func (m *Machine) IsBlocked(newMessages bool) machine.BlockReason {
	newMessagesInt := 0
	if newMessages {
		newMessagesInt = 1
	}
	cBlockReason := C.machineIsBlocked(m.c, C.int(newMessagesInt))
	switch cBlockReason.blockType {
	case C.BLOCK_TYPE_NOT_BLOCKED:
		return nil
	case C.BLOCK_TYPE_HALT:
		return machine.HaltBlocked{}
	case C.BLOCK_TYPE_ERROR:
		return machine.ErrorBlocked{}
	case C.BLOCK_TYPE_BREAKPOINT:
		return machine.BreakpointBlocked{}
	case C.BLOCK_TYPE_INBOX:
		return machine.InboxBlocked{}
	default:
	}
	return nil
}

func (m *Machine) PrintState() {
	C.machinePrint(m.c)
}

func makeExecutionAssertion(assertion C.RawAssertion) (*protocol.ExecutionAssertion, []value.Value, uint64) {
	sendsRaw := receiveByteSlice(assertion.sends)
	logsRaw := receiveByteSlice(assertion.logs)
	debugPrints := protocol.BytesArrayToVals(receiveByteSlice(assertion.debugPrints), uint64(assertion.debugPrintCount))
	return protocol.NewExecutionAssertion(
		uint64(assertion.numGas),
		uint64(assertion.inbox_messages_consumed),
		sendsRaw,
		uint64(assertion.sendCount),
		logsRaw,
		uint64(assertion.logCount),
	), debugPrints, uint64(assertion.numSteps)
}

func (m *Machine) ExecuteAssertion(
	maxGas uint64,
	goOverGas bool,
	messages []inbox.InboxMessage,
	finalMessageOfBlock bool,
) (*protocol.ExecutionAssertion, []value.Value, uint64) {
	goOverGasInt := C.uchar(0)
	if goOverGas {
		goOverGasInt = 1
	}

	finalMessageOfBlockInt := C.uchar(0)
	if finalMessageOfBlock {
		finalMessageOfBlockInt = 1
	}

	rawInboxData := encodeInboxMessages(messages)
	byteSlices := encodeByteSliceList(rawInboxData)
	sliceArrayData := C.malloc(C.size_t(C.sizeof_struct_ByteSliceStruct * len(byteSlices)))
	sliceArray := (*[1 << 30]C.struct_ByteSliceStruct)(sliceArrayData)[:len(byteSlices):len(byteSlices)]
	for i, data := range byteSlices {
		sliceArray[i] = data
	}
	defer C.free(sliceArrayData)
	msgData := C.struct_ByteSliceArrayStruct{slices: sliceArrayData, count: C.int(len(byteSlices))}

	assertion := C.executeAssertion(
		m.c,
		C.uint64_t(maxGas),
		C.int(goOverGasInt),
		msgData,
		C.int(finalMessageOfBlockInt),
	)

	return makeExecutionAssertion(assertion)
}

func (m *Machine) MarshalForProof() ([]byte, error) {
	rawProof := C.machineMarshallForProof(m.c)
	bytes := receiveByteSlice(rawProof)
	if len(bytes) == 0 {
		return nil, errors.Errorf("Error marshaling machine for proof")
	}
	return bytes, nil
}

func (m *Machine) MarshalBufferProof() ([]byte, error) {
	rawProof := C.machineMarshallBufferProof(m.c)
	return receiveByteSlice(rawProof), nil
}

func (m *Machine) MarshalState() ([]byte, error) {
	stateData := C.machineMarshallState(m.c)
	return receiveByteSlice(stateData), nil
}

func (m *Machine) Checkpoint(storage machine.ArbStorage) bool {
	cArbStorage := storage.(*ArbStorage)
	success := C.checkpointMachine(m.c, cArbStorage.c)

	return success == 1
}
