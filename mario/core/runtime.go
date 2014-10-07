//
// Copyright (c) 2014 10X Technologies, Inc. All rights reserved.
//
// Mario runtime. This is where the action happens.
//
package core

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

//=============================================================================
// Metadata
//=============================================================================
type Metadata struct {
	fqname    string
	path      string
	contents  map[string]bool
	filesPath string
	mutex     *sync.Mutex
}

func NewMetadata(fqname string, p string) *Metadata {
	self := &Metadata{}
	self.fqname = fqname
	self.path = p
	self.contents = map[string]bool{}
	self.filesPath = path.Join(p, "files")
	self.mutex = &sync.Mutex{}
	return self
}

func (self *Metadata) glob() []string {
	paths, _ := filepath.Glob(path.Join(self.path, "_*"))
	return paths
}

func (self *Metadata) enumerateFiles() ([]string, error) {
	return filepath.Glob(path.Join(self.filesPath, "*"))
}

func (self *Metadata) mkdirs() {
	// When making/remaking dirs, clear the cache.
	self.mutex.Lock()
	self.contents = map[string]bool{}
	self.mutex.Unlock()
	mkdir(self.path)
	mkdir(self.filesPath)
}

func (self *Metadata) idemMkdirs() {
	// When making/remaking dirs, clear the cache.
	self.mutex.Lock()
	self.contents = map[string]bool{}
	self.mutex.Unlock()
	idemMkdir(self.path)
	idemMkdir(self.filesPath)
}

func (self *Metadata) getState(name string) (string, bool) {
	if self.exists("errors") {
		return "failed", true
	}
	if self.exists("complete") {
		return name + "complete", true
	}
	if self.exists("log") {
		return name + "running", true
	}
	if self.exists("jobinfo") {
		return name + "queued", true
	}
	return "", false
}

func (self *Metadata) cache() {
	if !self.exists("complete") {
		paths := self.glob()
		self.mutex.Lock()
		self.contents = map[string]bool{}
		for _, p := range paths {
			self.contents[path.Base(p)[1:]] = true
		}
		self.mutex.Unlock()
	}
}

func (self *Metadata) makePath(name string) string {
	return path.Join(self.path, "_"+name)
}
func (self *Metadata) exists(name string) bool {
	self.mutex.Lock()
	_, ok := self.contents[name]
	self.mutex.Unlock()
	return ok
}
func (self *Metadata) readRaw(name string) string {
	bytes, _ := ioutil.ReadFile(self.makePath(name))
	return string(bytes)
}
func (self *Metadata) read(name string) interface{} {
	var v interface{}
	json.Unmarshal([]byte(self.readRaw(name)), &v)
	return v
}
func (self *Metadata) writeRaw(name string, text string) {
	ioutil.WriteFile(self.makePath(name), []byte(text), 0644)
}
func (self *Metadata) write(name string, object interface{}) {
	bytes, _ := json.MarshalIndent(object, "", "    ")
	self.writeRaw(name, string(bytes))
}
func (self *Metadata) append(name string, text string) {
	f, _ := os.OpenFile(self.makePath(name), os.O_WRONLY|os.O_CREATE, 0644)
	f.Write([]byte(text))
	f.Close()
}
func (self *Metadata) writeTime(name string) {
	self.writeRaw(name, Timestamp())
}
func (self *Metadata) remove(name string) { os.Remove(self.makePath(name)) }

func (self *Metadata) serialize() interface{} {
	names := []string{}
	self.mutex.Lock()
	for content, _ := range self.contents {
		names = append(names, content)
	}
	self.mutex.Unlock()
	sort.Strings(names)
	return map[string]interface{}{
		"path":  self.path,
		"names": names,
	}
}

//=============================================================================
// Binding
//=============================================================================
type Binding struct {
	node      *Node
	id        string
	tname     string
	sweep     bool
	waiting   bool
	valexp    string
	mode      string
	boundNode Nodable
	output    string
	value     interface{}
}

func NewBinding(node *Node, bindStm *BindStm) *Binding {
	self := &Binding{}
	self.node = node
	self.id = bindStm.id
	self.tname = bindStm.tname
	self.sweep = bindStm.sweep
	self.waiting = false
	switch valueExp := bindStm.exp.(type) {
	case *RefExp:
		if valueExp.kind == "self" {
			parentBinding := self.node.parent.getNode().argbindings[valueExp.id]
			if parentBinding != nil {
				self.node = parentBinding.node
				self.tname = parentBinding.tname
				self.sweep = parentBinding.sweep
				self.waiting = parentBinding.waiting
				self.mode = parentBinding.mode
				self.boundNode = parentBinding.boundNode
				self.output = parentBinding.output
				self.value = parentBinding.value
			}
			self.id = bindStm.id
			self.valexp = "self." + valueExp.id
		} else if valueExp.kind == "call" {
			self.mode = "reference"
			self.boundNode = self.node.parent.getNode().subnodes[valueExp.id]
			self.output = valueExp.outputId
			if valueExp.outputId == "default" {
				self.valexp = valueExp.id
			} else {
				self.valexp = valueExp.id + "." + valueExp.outputId
			}
		}
	case *ValExp:
		self.mode = "value"
		self.boundNode = node
		self.value = expToInterface(bindStm.exp)
	}
	return self
}

func expToInterface(exp Exp) interface{} {
	// Convert tree of Exps into a tree of interface{}s.
	valExp, ok := exp.(*ValExp)
	if !ok {
		return nil
	}
	if valExp.kind == "array" {
		varray := []interface{}{}
		for _, exp := range valExp.value.([]Exp) {
			varray = append(varray, expToInterface(exp))
		}
		return varray
	} else if valExp.kind == "map" {
		vmap := map[string]interface{}{}
		for k, exp := range valExp.value.(map[string]Exp) {
			vmap[k] = expToInterface(exp)
		}
		return vmap
	} else {
		return valExp.value
	}
}

func NewReturnBinding(node *Node, bindStm *BindStm) *Binding {
	self := &Binding{}
	self.node = node
	self.id = bindStm.id
	self.tname = bindStm.tname
	self.mode = "reference"
	valueExp := bindStm.exp.(*RefExp)
	self.boundNode = self.node.subnodes[valueExp.id] // from node, NOT parent; this is diff from Binding
	self.output = valueExp.outputId
	if valueExp.outputId == "default" {
		self.valexp = valueExp.id
	} else {
		self.valexp = valueExp.id + "." + valueExp.outputId
	}
	return self
}

func (self *Binding) resolve(argPermute map[string]interface{}) interface{} {
	self.waiting = false
	if self.mode == "value" {
		if argPermute == nil {
			// In this case we want to get the raw value, which might be a sweep array.
			return self.value
		}
		// Replace literal sweep ranges with specific permuted argument values.
		if self.sweep {
			return argPermute[self.id]
		} else {
			return self.value
		}
	}
	if argPermute == nil {
		return nil
	}
	if self.boundNode != nil {
		matchedFork := self.boundNode.getNode().matchFork(argPermute)
		outputs, ok := matchedFork.metadata.read("outs").(map[string]interface{})
		if ok {
			output, ok := outputs[self.output]
			if ok {
				return output
			}
		}
	}
	self.waiting = true
	return nil
}

func (self *Binding) serialize(argPermute map[string]interface{}) interface{} {
	var node interface{} = nil
	var matchedFork interface{} = nil
	if self.boundNode != nil {
		node = self.boundNode.getNode().name
		f := self.boundNode.getNode().matchFork(argPermute)
		if f != nil {
			matchedFork = f.index
		}
	}
	return map[string]interface{}{
		"id":          self.id,
		"type":        self.tname,
		"valexp":      self.valexp,
		"mode":        self.mode,
		"output":      self.output,
		"sweep":       self.sweep,
		"node":        node,
		"matchedFork": matchedFork,
		"value":       self.resolve(argPermute),
		"waiting":     self.waiting,
	}
}

// Helpers
func resolveBindings(bindings map[string]*Binding, argPermute map[string]interface{}) map[string]interface{} {
	resolvedBindings := map[string]interface{}{}
	for id, binding := range bindings {
		resolvedBindings[id] = binding.resolve(argPermute)
	}
	return resolvedBindings
}

func makeOutArgs(outParams *Params, filesPath string) map[string]interface{} {
	args := map[string]interface{}{}
	for id, param := range outParams.table {
		if param.getIsFile() {
			args[id] = path.Join(filesPath, param.getId()+"."+param.getTname())
		} else if param.getTname() == "path" {
			args[id] = path.Join(filesPath, param.getId())
		} else {
			args[id] = nil
		}
	}
	return args
}

//=============================================================================
// Chunk
//=============================================================================
type Chunk struct {
	node       *Node
	fork       *Fork
	index      int
	chunkDef   map[string]interface{}
	path       string
	fqname     string
	metadata   *Metadata
	hasBeenRun bool
}

func NewChunk(nodable Nodable, fork *Fork, index int, chunkDef map[string]interface{}) *Chunk {
	self := &Chunk{}
	self.node = nodable.getNode()
	self.fork = fork
	self.index = index
	self.chunkDef = chunkDef
	self.path = path.Join(fork.path, fmt.Sprintf("chnk%d", index))
	self.fqname = fork.fqname + fmt.Sprintf(".chnk%d", index)
	self.metadata = NewMetadata(self.fqname, self.path)
	self.hasBeenRun = false
	if !self.node.split {
		// If we're not splitting, just set the sole chunk's filesPath
		// to the filesPath of the parent fork, to save a pseudo-join copy.
		self.metadata.filesPath = self.fork.metadata.filesPath
	}
	// We have to mkdirs here because runtime might have been interrupted after chunk_defs were
	// written but before next step interval caused the actual creation of the chnk folders.
	// in that scenario, upon restart the fork step would try to write _args into chnk folders
	// that don't exist.
	// This also gets run if we are restarting from a failed stage.
	self.mkdirs()
	return self
}

func (self *Chunk) mkdirs() {
	self.metadata.idemMkdirs()
}

func (self *Chunk) getState() string {
	if state, ok := self.metadata.getState(""); ok {
		return state
	} else {
		return "ready"
	}
}

func (self *Chunk) step() {
	if self.getState() != "ready" {
		return
	}

	// Belt and suspenders for not double-submitting a job.
	if self.hasBeenRun {
		return
	} else {
		self.hasBeenRun = true
	}

	//
	// Process __threads and __mem_gb requested by stage split.
	//
	// __threads tells scheduler how much concurrency this chunk wants.
	// __mem_gb  tells SGE to kill-if-exceed. For local mode, it is
	//           instead a consumption request like __threads.

	// A chunk consumes 1 thread unless stage split explicitly asks for more.
	threads := 1
	if v, ok := self.chunkDef["__threads"].(float64); ok {
		threads = int(v)

		// In local mode, cap to the scheduler's max cores.
		// It is not sufficient for the scheduler to do the capping downstream.
		// We rewrite the chunkDef here to inform the chunk it should use less
		// concurrency.
		if self.node.rt.jobMode == "local" {
			maxCores := self.node.rt.scheduler.getMaxCores()
			if threads > maxCores {
				threads = maxCores
			}
			self.chunkDef["__threads"] = threads
		}
	}

	// Default to -1 to impose no limit (no flag will be passed to SGE).
	// The local mode scheduler will convert -1 to 1 downstream.
	memGB := -1
	if v, ok := self.chunkDef["__mem_gb"].(float64); ok {
		memGB = int(v)

		if self.node.rt.jobMode == "local" {
			maxMemGB := self.node.rt.scheduler.getMaxMemGB()
			if memGB > maxMemGB {
				memGB = maxMemGB
			}
			self.chunkDef["__mem_gb"] = memGB
		}
	}

	// Resolve input argument bindings and merge in the chunk defs.
	resolvedBindings := resolveBindings(self.node.argbindings, self.fork.argPermute)
	for id, value := range self.chunkDef {
		resolvedBindings[id] = value
	}

	// Write out input and ouput args for the chunk.
	self.metadata.write("args", resolvedBindings)
	self.metadata.write("outs", makeOutArgs(self.node.outparams, self.metadata.filesPath))

	// Run the chunk.
	self.node.runChunk(self.fqname, self.metadata, threads, memGB)
}

func (self *Chunk) serialize() interface{} {
	return map[string]interface{}{
		"index":    self.index,
		"chunkDef": self.chunkDef,
		"state":    self.getState(),
		"metadata": self.metadata.serialize(),
	}
}

//=============================================================================
// Fork
//=============================================================================
type Fork struct {
	node           *Node
	index          int
	path           string
	fqname         string
	metadata       *Metadata
	split_metadata *Metadata
	join_metadata  *Metadata
	chunks         []*Chunk
	split_has_run  bool
	join_has_run   bool
	argPermute     map[string]interface{}
}

func NewFork(nodable Nodable, index int, argPermute map[string]interface{}) *Fork {
	self := &Fork{}
	self.node = nodable.getNode()
	self.index = index
	self.path = path.Join(self.node.path, fmt.Sprintf("fork%d", index))
	self.fqname = self.node.fqname + fmt.Sprintf(".fork%d", index)
	self.metadata = NewMetadata(self.fqname, self.path)
	self.split_metadata = NewMetadata(self.fqname+".split", path.Join(self.path, "split"))
	self.join_metadata = NewMetadata(self.fqname+".join", path.Join(self.path, "join"))
	self.argPermute = argPermute
	self.split_has_run = false
	self.join_has_run = false
	// reconstruct chunks using chunk_defs on reattach, do not rely
	// on metadata.exists('chunk_defs') since it may not be cached
	self.chunks = []*Chunk{}
	chunkDefIfaces := self.split_metadata.read("chunk_defs")
	if chunkDefs, ok := chunkDefIfaces.([]interface{}); ok {
		for i, chunkDef := range chunkDefs {
			chunk := NewChunk(self.node, self, i, chunkDef.(map[string]interface{}))
			self.chunks = append(self.chunks, chunk)
		}
	}
	return self
}

func (self *Fork) clearChunks() {
	self.chunks = []*Chunk{}
}

func (self *Fork) collectMetadatas() []*Metadata {
	metadatas := []*Metadata{self.metadata, self.split_metadata, self.join_metadata}
	for _, chunk := range self.chunks {
		metadatas = append(metadatas, chunk.metadata)
	}
	return metadatas
}

func (self *Fork) mkdirs() {
	self.metadata.mkdirs()
	self.split_metadata.mkdirs()
	self.join_metadata.mkdirs()
}

func (self *Fork) getState() string {
	if self.metadata.exists("complete") {
		return "complete"
	}
	if state, ok := self.join_metadata.getState("join_"); ok {
		return state
	}
	if len(self.chunks) > 0 {
		// If any chunks have failed, we're failed.
		for _, chunk := range self.chunks {
			if chunk.getState() == "failed" {
				return "failed"
			}
		}
		// If every chunk is complete, we're complete.
		every := true
		for _, chunk := range self.chunks {
			if chunk.getState() != "complete" {
				every = false
				break
			}
		}
		if every {
			return "chunks_complete"
		}
		// If every chunk is queued, running, or complete, we're complete.
		every = true
		runningStates := map[string]bool{"queued": true, "running": true, "complete": true}
		for _, chunk := range self.chunks {
			if _, ok := runningStates[chunk.getState()]; !ok {
				every = false
				break
			}
		}
		if every {
			return "chunks_running"
		}
	}
	if state, ok := self.split_metadata.getState("split_"); ok {
		return state
	}
	return "ready"
}

func (self *Fork) step() {
	if self.node.kind == "stage" {
		state := self.getState()
		if !strings.HasSuffix(state, "_running") && !strings.HasSuffix(state, "_queued") {
			statePad := strings.Repeat(" ", 15-len(state))
			LogInfo("runtime", "(%s)%s %s", state, statePad, self.node.fqname)
		}

		if state == "ready" {
			self.split_metadata.write("args", resolveBindings(self.node.argbindings, self.argPermute))
			if self.node.split {
				if !self.split_has_run {
					self.split_has_run = true
					// Default memory to -1 for no limit.
					self.node.runSplit(self.fqname, self.split_metadata)
				}
			} else {
				self.split_metadata.write("chunk_defs", []interface{}{map[string]interface{}{}})
				self.split_metadata.writeTime("complete")
			}
		} else if state == "split_complete" {
			chunkDefs := self.split_metadata.read("chunk_defs")
			if len(self.chunks) == 0 {
				for i, chunkDef := range chunkDefs.([]interface{}) {
					chunk := NewChunk(self.node, self, i, chunkDef.(map[string]interface{}))
					self.chunks = append(self.chunks, chunk)
					chunk.mkdirs()
				}
			}
			for _, chunk := range self.chunks {
				chunk.step()
			}
		} else if state == "chunks_complete" {
			self.join_metadata.write("args", resolveBindings(self.node.argbindings, self.argPermute))
			self.join_metadata.write("chunk_defs", self.split_metadata.read("chunk_defs"))
			if self.node.split {
				chunkOuts := []interface{}{}
				for _, chunk := range self.chunks {
					outs := chunk.metadata.read("outs")
					chunkOuts = append(chunkOuts, outs)
				}
				self.join_metadata.write("chunk_outs", chunkOuts)
				self.join_metadata.write("outs", makeOutArgs(self.node.outparams, self.metadata.filesPath))
				if !self.join_has_run {
					self.join_has_run = true
					self.node.runJoin(self.fqname, self.join_metadata)
				}
			} else {
				self.join_metadata.write("outs", self.chunks[0].metadata.read("outs"))
				self.join_metadata.writeTime("complete")
			}
		} else if state == "join_complete" {
			self.metadata.write("outs", self.join_metadata.read("outs"))
			self.metadata.writeTime("complete")
		}

	} else if self.node.kind == "pipeline" {
		self.metadata.write("outs", resolveBindings(self.node.retbindings, self.argPermute))
		self.metadata.writeTime("complete")
	}
}

func (self *Fork) serialize() interface{} {
	argbindings := []interface{}{}
	for _, argbinding := range self.node.argbindingList {
		argbindings = append(argbindings, argbinding.serialize(self.argPermute))
	}
	retbindings := []interface{}{}
	for _, retbinding := range self.node.retbindingList {
		retbindings = append(retbindings, retbinding.serialize(self.argPermute))
	}
	bindings := map[string]interface{}{
		"Argument": argbindings,
		"Return":   retbindings,
	}
	chunks := []interface{}{}
	for _, chunk := range self.chunks {
		chunks = append(chunks, chunk.serialize())
	}
	return map[string]interface{}{
		"index":          self.index,
		"argPermute":     self.argPermute,
		"state":          self.getState(),
		"metadata":       self.metadata.serialize(),
		"split_metadata": self.split_metadata.serialize(),
		"join_metadata":  self.join_metadata.serialize(),
		"chunks":         chunks,
		"bindings":       bindings,
	}
}

//=============================================================================
// Node
//=============================================================================
type Nodable interface {
	getNode() *Node
}

type Node struct {
	parent         Nodable
	rt             *Runtime
	kind           string
	name           string
	fqname         string
	path           string
	metadata       *Metadata
	outparams      *Params
	argbindings    map[string]*Binding
	argbindingList []*Binding // for stable ordering
	retbindings    map[string]*Binding
	retbindingList []*Binding // for stable ordering
	sweepbindings  []*Binding
	subnodes       map[string]Nodable
	prenodes       map[string]Nodable
	prenodeList    []Nodable //for stable ordering
	forks          []*Fork
	split          bool
	state          string
	volatile       bool
	stagecodeLang  string
	stagecodePath  string
}

func (self *Node) getNode() *Node { return self }

func NewNode(parent Nodable, kind string, callStm *CallStm, callables *Callables) *Node {
	self := &Node{}
	self.parent = parent

	self.rt = parent.getNode().rt
	self.kind = kind
	self.name = callStm.id
	self.fqname = parent.getNode().fqname + "." + self.name
	self.path = path.Join(parent.getNode().path, self.name)
	self.metadata = NewMetadata(self.fqname, self.path)
	self.volatile = callStm.volatile

	self.outparams = callables.table[self.name].getOutParams()
	self.argbindings = map[string]*Binding{}
	self.argbindingList = []*Binding{}
	self.retbindings = map[string]*Binding{}
	self.retbindingList = []*Binding{}
	self.subnodes = map[string]Nodable{}
	self.prenodes = map[string]Nodable{}
	self.prenodeList = []Nodable{}

	for id, bindStm := range callStm.bindings.table {
		binding := NewBinding(self, bindStm)
		self.argbindings[id] = binding
		self.argbindingList = append(self.argbindingList, binding)
	}
	for _, binding := range self.argbindingList {
		if binding.mode == "reference" && binding.boundNode != nil {
			self.prenodes[binding.boundNode.getNode().name] = binding.boundNode
			self.prenodeList = append(self.prenodeList, binding.boundNode)
		}
	}
	// Do not set state = getState here, or else nodes will wrongly report
	// complete before the first refreshMetadata call
	return self
}

//
// Folder construction
//
func (self *Node) mkdirs(wg *sync.WaitGroup) {
	mkdir(self.path)
	for _, fork := range self.forks {
		wg.Add(1)
		go func(f *Fork) {
			f.mkdirs()
			wg.Done()
		}(fork)
	}
	for _, subnode := range self.subnodes {
		wg.Add(1)
		go func(n Nodable) {
			n.getNode().mkdirs(wg)
			wg.Done()
		}(subnode)
	}
}

//
// Sweep management
//
func (self *Node) buildForks(bindings map[string]*Binding) {
	// Use a map to uniquify bindings by id.
	bindingTable := map[string]*Binding{}

	// Add local sweep bindings.
	for _, binding := range bindings {
		if binding.sweep {
			bindingTable[binding.id] = binding
		}
	}
	// Add upstream sweep bindings (from prenodes).
	for _, prenode := range self.prenodes {
		for _, binding := range prenode.getNode().sweepbindings {
			bindingTable[binding.id] = binding
		}
	}

	for _, binding := range bindingTable {
		self.sweepbindings = append(self.sweepbindings, binding)
	}

	// Add all unique bindings to self.sweepbindings.
	paramIds := []string{}
	argRanges := []interface{}{}
	for _, binding := range self.sweepbindings {
		//  self.sweepbindings = append(self.sweepbindings, binding)
		paramIds = append(paramIds, binding.id)
		argRanges = append(argRanges, binding.resolve(nil))
	}

	// Build out argument permutations.
	for i, valPermute := range cartesianProduct(argRanges) {
		argPermute := map[string]interface{}{}
		for j, paramId := range paramIds {
			argPermute[paramId] = valPermute.([]interface{})[j]
		}
		self.forks = append(self.forks, NewFork(self, i, argPermute))
	}
}

func (self *Node) matchFork(targetArgPermute map[string]interface{}) *Fork {
	if targetArgPermute == nil {
		return nil
	}
	for _, fork := range self.forks {
		every := true
		for paramId, argValue := range fork.argPermute {
			if targetArgPermute[paramId] != argValue {
				every = false
				break
			}
		}
		if every {
			return fork
		}
	}
	return nil
}

//
// Subnode management
//
func (self *Node) allNodes() []*Node {
	all := []*Node{self}
	for _, subnode := range self.subnodes {
		all = append(all, subnode.getNode().allNodes()...)
	}
	return all
}

func (self *Node) find(fqname string) *Node {
	if self.fqname == fqname {
		return self
	}
	for _, subnode := range self.subnodes {
		node := subnode.getNode().find(fqname)
		if node != nil {
			return node
		}
	}
	return nil
}

//
// State management
//
func (self *Node) collectMetadatas() []*Metadata {
	metadatas := []*Metadata{self.metadata}
	for _, fork := range self.forks {
		metadatas = append(metadatas, fork.collectMetadatas()...)
	}
	return metadatas
}

func (self *Node) refreshMetadata() {
	metadatas := self.collectMetadatas()
	for _, metadata := range metadatas {
		metadata.cache()
	}
	self.state = self.getState()
}

func (self *Node) getState() string {
	// If every fork is complete, we're complete.
	complete := true
	for _, fork := range self.forks {
		if fork.getState() != "complete" {
			complete = false
			break
		}
	}
	if complete {
		return "complete"
	}
	// If any fork is failed, we're failed.
	for _, fork := range self.forks {
		if fork.getState() == "failed" {
			return "failed"
		}
	}
	// If any prenode is not complete, we're waiting.
	for _, prenode := range self.prenodes {
		if prenode.getNode().getState() != "complete" {
			return "waiting"
		}
	}
	// Otherwise we're running.
	return "running"
}

func (self *Node) restartFromFailed() {
	// Blow away the entire stage node.
	os.RemoveAll(self.path)

	// Re-create the folders.
	// This will also clear all the metadata in-memory caches.
	var rewg sync.WaitGroup
	self.mkdirs(&rewg)
	rewg.Wait()

	// Refresh the metadata (clear it all).
	self.refreshMetadata()

	// Clear chunks in the forks so they can be rebuilt on split.
	for _, fork := range self.forks {
		fork.clearChunks()
	}
}

func (self *Node) getFatalError() (string, string, string, string) {
	for _, metadata := range self.collectMetadatas() {
		if !metadata.exists("errors") {
			continue
		}
		errlog := metadata.readRaw("errors")
		summary := "<none>"
		if self.stagecodeLang == "Python" {
			errlines := strings.Split(errlog, "\n")
			if len(errlines) >= 2 {
				summary = errlines[len(errlines)-2]
			}
		}
		return metadata.fqname, metadata.makePath("errors"),
			summary, errlog
	}
	return "", "", "", ""
}

func (self *Node) step() {
	if self.state == "running" {
		for _, fork := range self.forks {
			fork.step()
		}
	}
}

//
// Serialization
//
func (self *Node) serialize() interface{} {
	sweepbindings := []interface{}{}
	for _, sweepbinding := range self.sweepbindings {
		sweepbindings = append(sweepbindings, sweepbinding.serialize(nil))
	}
	forks := []interface{}{}
	for _, fork := range self.forks {
		forks = append(forks, fork.serialize())
	}
	edges := []interface{}{}
	for _, prenode := range self.prenodeList {
		edges = append(edges, map[string]string{
			"from": prenode.getNode().name,
			"to":   self.name,
		})
	}
	var err interface{} = nil
	if self.state == "failed" {
		fqname, errpath, summary, log := self.getFatalError()
		err = map[string]string{
			"fqname":  fqname,
			"path":    errpath,
			"summary": summary,
			"log":     log,
		}
	}
	return map[string]interface{}{
		"name":          self.name,
		"fqname":        self.fqname,
		"type":          self.kind,
		"path":          self.path,
		"state":         self.state,
		"metadata":      self.metadata.serialize(),
		"sweepbindings": sweepbindings,
		"forks":         forks,
		"edges":         edges,
		"stagecodeLang": self.stagecodeLang,
		"stagecodePath": self.stagecodePath,
		"error":         err,
	}
}

//=============================================================================
// Job Runners
//=============================================================================
func (self *Node) runSplit(fqname string, metadata *Metadata) {
	self.runJob("split", fqname, metadata, 1, -1)
}

func (self *Node) runJoin(fqname string, metadata *Metadata) {
	self.runJob("join", fqname, metadata, 1, -1)
}

func (self *Node) runChunk(fqname string, metadata *Metadata, threads int, memGB int) {
	self.runJob("main", fqname, metadata, threads, memGB)
}

func (self *Node) runJob(shellName string, fqname string, metadata *Metadata,
	threads int, memGB int) {

	// Log the job run.
	modePad := strings.Repeat(" ", 15-(len(self.rt.jobMode)+4))
	LogInfo("runtime", "(run:%s)%s %s.%s", self.rt.jobMode, modePad, fqname, shellName)
	metadata.write("jobinfo", map[string]interface{}{"type": nil, "childpid": nil})

	// Construct path to the shell.
	shellCmd := path.Join(self.rt.adaptersPath, "python", shellName+".py")

	// Configure profiling.
	profile := "disable"
	if self.rt.enableProfiling {
		profile = "profile"
	}

	switch self.rt.jobMode {
	case "local":
		self.execLocalJob(shellCmd, self.stagecodePath, metadata, threads, memGB, profile)
	case "sge":
		self.execSGEJob(fqname, shellName, shellCmd, self.stagecodePath, metadata, threads, memGB, profile)
	default:
		panic(fmt.Sprintf("Unknown jobMode: %s", self.rt.jobMode))
	}
}

func (self *Node) execLocalJob(shellCmd string, stagecodePath string,
	metadata *Metadata, threads int, memGB int, profile string) {

	// Exec the shell directly.
	argv := []string{stagecodePath, metadata.path, metadata.filesPath, profile}
	cmd := exec.Command(shellCmd, argv...)

	// Connect child to _stdout and _stderr metadata files.
	stdoutFile, _ := os.Create(metadata.makePath("stdout"))
	stderrFile, _ := os.Create(metadata.makePath("stderr"))
	stdoutFile.WriteString("[stdout]\n")
	stderrFile.WriteString("[stderr]\n")
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	errorsPath := metadata.makePath("errors")

	// Enqueue the command to the local scheduler.
	self.rt.scheduler.Enqueue(cmd, threads, memGB, stdoutFile, stderrFile, errorsPath)
}

func (self *Node) execSGEJob(fqname string, shellName string, shellCmd string,
	stagecodePath string, metadata *Metadata, threads int, memGB int, profile string) {

	// Generate the script that will be qsub'ed.
	argv := []string{shellCmd, stagecodePath, metadata.path, metadata.filesPath, profile}
	metadata.writeRaw("qscript", strings.Join(argv, " "))

	// Sanity check the thread count.
	if threads < 1 {
		threads = 1
	}

	// Build the qsub command.
	argv = []string{
		"-N", fqname + "." + shellName,
		"-V",
		"-pe", "threads", fmt.Sprintf("%d", threads),
	}
	// Only append memory cap if value is sane.
	if memGB > 0 {
		argv = append(argv, "-l", fmt.Sprintf("h_vmem=%dG", memGB))
	}
	argv = append(argv,
		"-cwd",
		"-o", metadata.makePath("stdout"),
		"-e", metadata.makePath("stderr"),
		metadata.makePath("qscript"),
	)

	metadata.write("jobinfo", map[string]string{"type": "sge"})

	// Exec the qsub command synchronously and write result out to _qsub.
	cmd := exec.Command("qsub", argv...)
	cmd.Dir = metadata.filesPath
	out := ""
	if data, err := cmd.CombinedOutput(); err == nil {
		out = string(data)
	} else {
		out = err.Error()
	}
	metadata.writeRaw("qsub", strings.Join(cmd.Args, " ")+"\n\n"+out)
}

//=============================================================================
// Stagestance
//=============================================================================
type Stagestance struct {
	node *Node
}

func NewStagestance(parent Nodable, callStm *CallStm, callables *Callables) *Stagestance {
	langMap := map[string]string{
		"py": "Python",
	}

	self := &Stagestance{}
	self.node = NewNode(parent, "stage", callStm, callables)
	stage, ok := callables.table[self.node.name].(*Stage)
	if !ok {
		return nil
	}
	self.node.stagecodePath = path.Join(self.node.rt.mroPath, stage.src.path)
	self.node.stagecodeLang = langMap[stage.src.lang]
	self.node.split = len(stage.splitParams.list) > 0
	self.node.buildForks(self.node.argbindings)
	return self
}

func (self *Stagestance) getNode() *Node   { return self.node }
func (self *Stagestance) RefreshMetadata() { self.getNode().refreshMetadata() }
func (self *Stagestance) GetState() string { return self.getNode().getState() }
func (self *Stagestance) Step()            { self.getNode().step() }
func (self *Stagestance) GetFatalError() (string, string, string, string) {
	return self.getNode().getFatalError()
}

//=============================================================================
// Pipestance
//=============================================================================
type Pipestance struct {
	node *Node
}

func NewPipestance(parent Nodable, callStm *CallStm, callables *Callables) *Pipestance {
	self := &Pipestance{}
	self.node = NewNode(parent, "pipeline", callStm, callables)

	// Build subcall tree.
	pipeline := callables.table[self.node.name].(*Pipeline)
	for _, subcallStm := range pipeline.calls {
		callable := callables.table[subcallStm.id]
		switch callable.(type) {
		case *Stage:
			self.node.subnodes[subcallStm.id] = NewStagestance(self.node, subcallStm, callables)
		case *Pipeline:
			self.node.subnodes[subcallStm.id] = NewPipestance(self.node, subcallStm, callables)
		}
	}

	// Also depends on stages bound to return values.
	self.node.retbindings = map[string]*Binding{}
	for id, bindStm := range pipeline.ret.bindings.table {
		binding := NewReturnBinding(self.node, bindStm)
		self.node.retbindings[id] = binding
		self.node.retbindingList = append(self.node.retbindingList, binding)
		if binding.mode == "reference" && binding.boundNode != nil {
			self.node.prenodes[binding.boundNode.getNode().name] = binding.boundNode
			self.node.prenodeList = append(self.node.prenodeList, binding.boundNode)
		}
	}

	self.node.buildForks(self.node.retbindings)
	return self
}

func (self *Pipestance) getNode() *Node    { return self.node }
func (self *Pipestance) GetPname() string  { return self.node.name }
func (self *Pipestance) GetPsid() string   { return self.node.parent.getNode().name }
func (self *Pipestance) GetFQName() string { return self.node.fqname }

func (self *Pipestance) RefreshMetadata() {
	// We used to make this concurrent but ended up with too many
	// goroutines (Pranav's 96-sample run).
	for _, node := range self.node.allNodes() {
		node.refreshMetadata()
	}
}

func (self *Pipestance) GetState() string {
	nodes := self.node.allNodes()
	for _, node := range nodes {
		if node.state == "failed" {
			return "failed"
		}
	}
	for _, node := range nodes {
		if node.state == "running" {
			return "running"
		}
	}
	every := true
	for _, node := range nodes {
		if node.state != "complete" {
			every = false
			break
		}
	}
	if every {
		return "complete"
	}
	return "waiting"
}

func (self *Pipestance) GetFatalError() (string, string, string, string) {
	nodes := self.node.allNodes()
	for _, node := range nodes {
		if node.state == "failed" {
			return node.getFatalError()
		}
	}
	return "", "", "", ""
}

func (self *Pipestance) StepNodes() {
	for _, node := range self.node.allNodes() {
		node.step()
	}
}

func (self *Pipestance) RestartFailedNode(fqname string) {
	self.node.find(fqname).restartFromFailed()
}

func (self *Pipestance) Serialize() interface{} {
	ser := []interface{}{}
	for _, node := range self.node.allNodes() {
		ser = append(ser, node.serialize())
	}
	return ser
}

func (self *Pipestance) Immortalize() {
	metadata := NewMetadata(self.node.parent.getNode().fqname,
		self.node.parent.getNode().path)
	metadata.write("finalstate", self.Serialize())
}

func (self *Pipestance) Unimmortalize() {
	metadata := NewMetadata(self.node.parent.getNode().fqname,
		self.node.parent.getNode().path)
	metadata.remove("finalstate")
}

func (self *Pipestance) GetOuts(forki int) interface{} {
	if v := self.getNode().forks[forki].metadata.read("outs"); v != nil {
		return v
	}
	return map[string]interface{}{}
}

type VDRKillReport struct {
	Count  uint     `json:"count"`
	Size   uint64   `json:"size"`
	Paths  []string `json:"paths"`
	Errors []string `json:"errors"`
}

func (self *Pipestance) VDRKill() *VDRKillReport {
	killPaths := []string{}

	// Iterate over all nodes.
	for _, node := range self.node.allNodes() {
		// Iterate over all forks.
		for _, fork := range node.forks {
			// For volatile nodes, kill fork-level files.
			if node.volatile {
				if paths, err := fork.metadata.enumerateFiles(); err == nil {
					killPaths = append(killPaths, paths...)
				}
				if paths, err := fork.split_metadata.enumerateFiles(); err == nil {
					killPaths = append(killPaths, paths...)
				}
				if paths, err := fork.join_metadata.enumerateFiles(); err == nil {
					killPaths = append(killPaths, paths...)
				}
			}
			// For ALL nodes, if the node splits, kill chunk-level files.
			// Must check for split here, otherwise we'll end up deleting
			// output files of non-volatile nodes because single-chunk nodes
			// get their output redirected to the one chunk's files path.
			if node.split {
				for _, chunk := range fork.chunks {
					if paths, err := chunk.metadata.enumerateFiles(); err == nil {
						killPaths = append(killPaths, paths...)
					}
				}
			}
		}
	}

	// Actually delete the paths.
	killReport := VDRKillReport{}
	for _, p := range killPaths {
		filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
			if err == nil {
				killReport.Size += uint64(info.Size())
				killReport.Count++
			} else {
				killReport.Errors = append(killReport.Errors, err.Error())
			}
			return nil
		})
		killReport.Paths = append(killReport.Paths, p)
		os.RemoveAll(p)
	}
	metadata := NewMetadata(self.node.parent.getNode().fqname,
		self.node.parent.getNode().path)
	metadata.write("vdrkill", &killReport)
	return &killReport
}

//=============================================================================
// TopNode
//=============================================================================
type TopNode struct {
	node *Node
}

func (self *TopNode) getNode() *Node { return self.node }

func NewTopNode(rt *Runtime, psid string, p string) *TopNode {
	self := &TopNode{}
	self.node = &Node{}
	self.node.path = p
	self.node.rt = rt
	self.node.fqname = "ID." + psid
	self.node.name = psid
	return self
}

//=============================================================================
// Runtime
//=============================================================================
type Runtime struct {
	mroPath         string
	adaptersPath    string
	marioVersion    string
	mroVersion      string
	pipelineTable   map[string]*Pipeline
	PipelineNames   []string
	jobMode         string
	scheduler       *Scheduler
	enableProfiling bool
}

func NewRuntime(jobMode string, mroPath string, marioVersion string,
	mroVersion string, enableProfiling bool) *Runtime {
	return NewRuntimeWithCores(jobMode, mroPath, marioVersion, mroVersion,
		-1, -1, enableProfiling)
}

func NewRuntimeWithCores(jobMode string, mroPath string, marioVersion string,
	mroVersion string, reqCores int, reqMem int, enableProfiling bool) *Runtime {

	self := &Runtime{}
	self.mroPath = mroPath
	self.adaptersPath = RelPath(path.Join("..", "adapters"))
	self.marioVersion = marioVersion
	self.mroVersion = mroVersion
	self.jobMode = jobMode
	self.scheduler = NewScheduler(reqCores, reqMem)
	self.enableProfiling = enableProfiling
	self.pipelineTable = map[string]*Pipeline{}
	self.PipelineNames = []string{}

	// Parse all MROs in MROPATH and cache pipelines by name.
	fpaths, _ := filepath.Glob(self.mroPath + "/[^_]*.mro")
	for _, fpath := range fpaths {
		if data, err := ioutil.ReadFile(fpath); err == nil {
			if _, ast, err := parseSource(string(data), fpath, []string{self.mroPath}, true); err == nil {
				for _, pipeline := range ast.pipelines {
					self.pipelineTable[pipeline.getId()] = pipeline
					self.PipelineNames = append(self.PipelineNames, pipeline.getId())
				}
			}
		}
	}
	return self
}

// Compile an MRO file in cwd or self.mroPath.
func (self *Runtime) Compile(fpath string, checkSrcPath bool) (string, *Ast, error) {
	if data, err := ioutil.ReadFile(fpath); err != nil {
		return "", nil, err
	} else {
		return parseSource(string(data), fpath, []string{self.mroPath}, checkSrcPath)
	}
}

// Compile all the MRO files in self.mroPath.
func (self *Runtime) CompileAll(checkSrcPath bool) (int, error) {
	fpaths, _ := filepath.Glob(self.mroPath + "/[^_]*.mro")
	for _, fpath := range fpaths {
		if _, _, err := self.Compile(fpath, checkSrcPath); err != nil {
			return 0, err
		}
	}
	return len(fpaths), nil
}

// Instantiate a pipestance object given a psid, MRO source, and a
// pipestance path. This is the core (private) method called by the
// public InvokeWithSource and Reattach methods.
func (self *Runtime) instantiatePipeline(src string, srcPath string, psid string,
	pipestancePath string) (string, *Pipestance, error) {
	// Parse the invocation source.
	postsrc, ast, err := parseSource(src, srcPath, []string{self.mroPath}, true)
	if err != nil {
		return "", nil, err
	}

	// Instantiate the pipeline.
	if ast.call == nil {
		return "", nil, &MarioError{"NoCallError: cannot start a pipeline without a call statement."}
	}
	pipestance := NewPipestance(NewTopNode(self, psid, pipestancePath), ast.call, ast.callables)
	return postsrc, pipestance, nil
}

// Invokes a new pipestance.
func (self *Runtime) InvokePipeline(src string, srcPath string, psid string,
	pipestancePath string) (*Pipestance, error) {

	// Error if pipestance exists, otherwise create.
	if _, err := os.Stat(pipestancePath); err == nil {
		return nil, &PipestanceExistsError{psid}
	} else if err := os.MkdirAll(pipestancePath, 0755); err != nil {
		return nil, err
	}

	// Expand env vars in invocation source and instantiate.
	src = os.ExpandEnv(src)
	postsrc, pipestance, err := self.instantiatePipeline(src, srcPath, psid, pipestancePath)
	if err != nil {
		// If instantiation failed, delete the pipestance folder.
		os.RemoveAll(pipestancePath)
		return nil, err
	}

	// Write top-level metadata files.
	metadata := NewMetadata("ID."+psid, pipestancePath)
	metadata.writeRaw("invocation", src)
	metadata.writeRaw("mrosource", postsrc)
	metadata.write("versions", map[string]string{
		"mario":     GetVersion(),
		"pipelines": GetGitTag(self.mroPath),
	})
	metadata.writeTime("timestamp")

	// Create pipestance folder graph concurrently.
	var wg sync.WaitGroup
	pipestance.getNode().mkdirs(&wg)
	wg.Wait()

	return pipestance, nil
}

// Reattaches to an existing pipestance.
func (self *Runtime) ReattachToPipestance(psid string, pipestancePath string) (*Pipestance, error) {
	fname := "_invocation"

	// Read in the existing _invocation file.
	data, err := ioutil.ReadFile(path.Join(pipestancePath, fname))
	if err != nil {
		return nil, err
	}

	// Instantiate the pipestance.
	_, pipestance, err := self.instantiatePipeline(string(data), fname, psid, pipestancePath)
	return pipestance, err
}

// Instantiate a stagestance.
func (self *Runtime) InvokeStage(src string, srcPath string, ssid string,
	stagestancePath string) (*Stagestance, error) {
	// Check if stagestance path already exists.
	if _, err := os.Stat(stagestancePath); err == nil {
		return nil, &StagestanceExistsError{ssid}
	} else if err := os.MkdirAll(stagestancePath, 0755); err != nil {
		return nil, err
	}

	// Parse the invocation source.
	src = os.ExpandEnv(src)
	_, ast, err := parseSource(src, srcPath, []string{self.mroPath}, true)
	if err != nil {
		return nil, err
	}

	// Create stagestance.
	if ast.call == nil {
		return nil, &MarioError{"NoCallError: cannot start a stage without a call statement."}
	}
	stagestance := NewStagestance(NewTopNode(self, "", stagestancePath), ast.call, ast.callables)
	if stagestance == nil {
		return nil, &MarioError{fmt.Sprintf("NotAStageError: '%s'", ast.call.id)}
	}

	// Create stagestance folder graph concurrently.
	var wg sync.WaitGroup
	stagestance.getNode().mkdirs(&wg)
	wg.Wait()

	return stagestance, nil
}

func (self *Runtime) GetSerialization(pipestancePath string) (interface{}, bool) {
	metadata := NewMetadata("", pipestancePath)
	metadata.cache()
	if metadata.exists("finalstate") {
		return metadata.read("finalstate"), true
	}
	return nil, false
}

/****************************************************************************
 * Used Only for MARSOC
 */
func (self *Runtime) buildVal(param Param, val interface{}) string {
	switch {
	case val == nil:
		return "null"

	case reflect.TypeOf(val).Kind() == reflect.Slice:
		a := []string{}
		slice := reflect.ValueOf(val)
		for i := 0; i < slice.Len(); i++ {
			v := slice.Index(i).Interface()
			a = append(a, self.buildVal(param, v))
		}
		return fmt.Sprintf("[%s]", strings.Join(a, ", "))

	case param.getIsFile():
		return fmt.Sprintf("\"%s\"", val)
	}

	switch param.getTname() {
	case "path", "string":
		return fmt.Sprintf("\"%s\"", val)
	case "float":
		return fmt.Sprintf("%f", val)
	case "bool":
		return fmt.Sprintf("%t", val)
	case "int":
		if fval, ok := val.(float64); ok {
			return fmt.Sprintf("%d", int(fval))
		}
	case "map":
		if data, err := json.Marshal(val); err == nil {
			return string(data)
		}
	}
	return fmt.Sprintf("%v", val)
}

func (self *Runtime) BuildCallSource(pname string, args map[string]interface{}) string {
	// Loop over the pipeline's in params and print a binding
	// whether the args bag has a value for it not.
	lines := []string{}
	for _, param := range self.pipelineTable[pname].getInParams().list {
		valstr := self.buildVal(param, args[param.getId()])
		lines = append(lines, fmt.Sprintf("    %s = %s,", param.getId(), valstr))
	}
	return fmt.Sprintf("@include \"%s.mro\"\n\ncall %s(\n%s\n)", strings.ToLower(pname),
		pname, strings.Join(lines, "\n"))
}