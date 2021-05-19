#!/bin/bash

# shellcheck disable=SC1091
. edksetup.sh

set -e

NUM_CPUS="$(getconf _NPROCESSORS_ONLN)"

case $(uname -m) in
    aarch64) build -b RELEASE -t GCC5 -a AARCH64 -p ArmVirtPkg/ArmVirtQemu.dsc -n "$NUM_CPUS"
             cp Build/ArmVirtQemu-AARCH64/RELEASE_GCC5/FV/QEMU_EFI.fd OVMF_CODE.fd
             cp Build/ArmVirtQemu-AARCH64/RELEASE_GCC5/FV/QEMU_VARS.fd OVMF_VARS.fd
             build -b RELEASE -t GCC5 -a AARCH64 -p ArmVirtPkg/ArmVirtXen.dsc -n "$NUM_CPUS"
             cp Build/ArmVirtXen-AARCH64/RELEASE_*/FV/XEN_EFI.fd OVMF_PVH.fd
             ;;
     x86_64) build -b RELEASE -t GCC5 -a X64 -p OvmfPkg/OvmfPkgX64.dsc -n "$NUM_CPUS"
             cp Build/OvmfX64/RELEASE_*/FV/OVMF*.fd .
             build -b RELEASE -t GCC5 -a X64 -p OvmfPkg/OvmfXen.dsc -n "$NUM_CPUS"
             cp Build/OvmfXen/RELEASE_*/FV/OVMF.fd OVMF_PVH.fd
             ;;
          *) echo "Unsupported architecture $(uname). Bailing."
             exit 1
             ;;
esac
