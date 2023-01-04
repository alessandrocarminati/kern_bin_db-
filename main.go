/*
 * ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 *
 *   Name: kern_bin_db - Kernel source code analysis tool database creator
 *   Description: Parses kernel source tree and binary images and builds the DB
 *
 *   Author: Alessandro Carminati <acarmina@redhat.com>
 *   Author: Maurizio Papini <mpapini@redhat.com>
 *
 * ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 *
 *   Copyright (c) 2022 Red Hat, Inc. All rights reserved.
 *
 *   This copyrighted material is made available to anyone wishing
 *   to use, modify, copy, or redistribute it subject to the terms
 *   and conditions of the GNU General Public License version 2.
 *
 *   This program is distributed in the hope that it will be
 *   useful, but WITHOUT ANY WARRANTY; without even the implied
 *   warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR
 *   PURPOSE. See the GNU General Public License for more details.
 *
 *   You should have received a copy of the GNU General Public
 *   License along with this program; if not, write to the Free
 *   Software Foundation, Inc., 51 Franklin Street, Fifth Floor,
 *   Boston, MA 02110-1301, USA.
 *
 * ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
 */
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/cheggaaa/pb/v3"
	r2 "github.com/radareorg/r2pipe-go"
)

// Bitfield configuration mode constants
const (
	ENABLE_SYBOLSNFILES   = 1
	ENABLE_XREFS          = 2
	ENABLE_MAINTAINERS    = 4
	ENABLE_VERSION_CONFIG = 8
)

func main() {
	var cache []xref_cache
	var r2p *r2.Pipe
	var bar *pb.ProgressBar
	var funcs_data []func_data
	var err error
	var count int
	var id int
	var addr2line_prefix string = ""

	conf, err := args_parse(cmd_line_item_init())
	if err != nil {
		fmt.Println("Kernel symbol fetcher")
		print_help(cmd_line_item_init())
		os.Exit(-1)
	}
	fmt.Println("create stripped version")
	strip(conf.StripBin, conf.LinuxWDebug, conf.LinuxWODebug)
	t := Connect_token{conf.DBURL, conf.DBPort, conf.DBUser, conf.DBPassword, conf.DBTargetDB}
	context := A2L_resolver__init(conf.LinuxWDebug, Connect_db(&t), false)
	if conf.Mode&(ENABLE_VERSION_CONFIG) != 0 {
		config, _ := get_FromFile(conf.KConfig_fn)
		makefile, _ := get_FromFile(conf.KMakefile)
		v, err := get_version(makefile)
		if err != nil {
			panic(err)
		}
		wl:=Workload{Workload_type: GENERATE_QUERY, Query_args: Insert_Instance_Args{v.Version, v.Patchlevel, v.Sublevel, v.Extraversion, conf.Note}}
		query_mgmt(&context, &wl}
		id = Insert_datawID(db, wl.Query_str)
		kconfig := parse_config(config)

		fmt.Println("store config")
		bar = pb.StartNew(len(kconfig))
		wl.Workload_type = GENERATE_QUERY_AND_EXECUTE
		for key, value := range kconfig {
			wl.Query_args = Insert_Config_Args{key, value, id}
			query_mgmt(&context, &wl)
			bar.Increment()
		}
		bar.Finish()
	}

	if conf.Mode&(ENABLE_SYBOLSNFILES|ENABLE_XREFS) != 0 {
		r2p, err = r2.NewPipe(conf.LinuxWODebug)
		if err != nil {
			panic(err)
		}
		wl.Workload_type = GENERATE_QUERY_AND_EXECUTE
		wl.Query_args = Insert_Files_Ind_Args{id}
		query_mgmt(&context, &wl)
		wl.Query_args = Insert_Symbols_Ind_Args{id}
		query_mgmt(&context, &wl)
		wl.Query_args = Insert_Tags_Ind_Args{id}
		query_mgmt(&context, &wl)
		fmt.Println("initialize analysis")
		init_fw(r2p)
		funcs_data = get_all_funcdata(r2p)
	}

	if conf.Mode&ENABLE_SYBOLSNFILES != 0 {
		count = len(funcs_data)
		bar = pb.StartNew(count)

		fmt.Println("collecting symbols & files")
		for _, a := range funcs_data {
			bar.Increment()
			symbtype := "direct"
			if a.Indirect {
				symbtype = "indirect"
			}
			if strings.Contains(a.Name, "sym.") || a.Indirect {
				wl=Workload{
					Workload_type:	GENERATE_QUERY_AND_EXECUTE_W_A2L,
					Addr2ln_offset:	a.Offset,
					Addr2ln_name:	strings.ReplaceAll(a.Name, "sym.", ""),
					Query_args:	Insert_Symbols_Files_Args{id, strings.ReplaceAll(a.Name, "sym.", ""), fmt.Sprintf("0x%08x", a.Offset), symbtype}
					}
				query_mgmt(&context, &wl)
			}

			// query for addr2line file prefix
			if a.Name == "sym.start_kernel" {
				var start_kernel_file_tail string = "init/main.c"
				start_kernel_file := strings.Split(resolve_addr(context, a.Offset), ":")
				if start_kernel_file[0] == "NONE" {
					panic("Error resolving start_kernel!")
				}
				if len(start_kernel_file[0]) > len(start_kernel_file_tail) {
					addr2line_prefix = start_kernel_file[0][:len(start_kernel_file)-len(start_kernel_file_tail)]
				}
			}
		}
		bar.Finish()
	}
	if conf.Mode&ENABLE_XREFS != 0 {
		fmt.Println("Collecting indrcalls")
		indcl := get_indirect_calls(r2p, funcs_data)
		fmt.Println("Collecting xref")
		bar = pb.StartNew(count)
		for _, a := range funcs_data {
			bar.Increment()
			if strings.Contains(a.Name, "sym.") {
				Move(r2p, a.Offset)
				xrefs := remove_non_func(Getxrefs(r2p, a.Offset, indcl, funcs_data, &cache), funcs_data)
				for _, l := range xrefs {
					source_ref := resolve_addr(context, l.From)
					wl.Workload_type = GENERATE_QUERY_AND_EXECUTE
					wl.Query_args = Insert_Xrefs_Args{Caller_Offset: a.Offset, Callee_Offset: l.To, Id: id, Source_line: source_ref, Calling_Offset: l.From}
					query_mgmt(&context, &wl)
				}
			}
		}
		bar.Finish()
	}
	wl.Workload_type = GENERATE_QUERY
	wl.Query_args = Insert_Tags_Args{addr2line_prefix}
	query_mgmt(&context, &wl)
	if conf.Mode&ENABLE_MAINTAINERS != 0 {
		fmt.Println("Collecting tags")
		s, err := get_FromFile(conf.Maintainers_fn)
		if err != nil {
			panic(err)
		}
		ss := s[seek2data(s):]
		items := parse_maintainers(ss)
		queries := generate_queries(conf.Maintainers_fn, items, wl.Query_str, id)
		bar = pb.StartNew(len(queries))
		wl.Workload_type = EXECUTE_QUERY_ONLY
		for _, q := range queries {
			bar.Increment()
			wl.Query_str = q
			query_mgmt(&context, &wl)
		}
		bar.Finish()
	}
}
