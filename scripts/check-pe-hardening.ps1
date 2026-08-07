#requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory, Position = 0, ValueFromRemainingArguments)]
    [ValidateNotNullOrEmpty()]
    [string[]]$Path
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ImageFileMachineAMD64 = 0x8664
$ImageNtOptionalHdr64Magic = 0x20b
$ImageDllCharacteristicsHighEntropyVA = 0x0020
$ImageDllCharacteristicsDynamicBase = 0x0040
$ImageDllCharacteristicsNXCompat = 0x0100
$ImageDllCharacteristicsGuardCF = 0x4000
$ImageScnMemExecute = [uint32]0x20000000
$ImageScnMemWrite = [uint32]0x80000000L

function Read-PEImage {
    param([Parameter(Mandatory)][string]$FilePath)

    $resolved = (Resolve-Path -LiteralPath $FilePath).Path
    $stream = [System.IO.File]::OpenRead($resolved)
    $reader = [System.IO.BinaryReader]::new($stream)
    try {
        if ($reader.ReadUInt16() -ne 0x5a4d) {
            throw "$resolved is not a PE image (missing MZ header)"
        }

        $stream.Position = 0x3c
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0 -or $peOffset -gt ($stream.Length - 24)) {
            throw "$resolved has an invalid PE header offset"
        }

        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "$resolved is not a PE image (missing PE signature)"
        }

        $machine = $reader.ReadUInt16()
        $sectionCount = $reader.ReadUInt16()
        $stream.Position += 12
        $optionalHeaderSize = $reader.ReadUInt16()
        $stream.Position += 2
        $optionalHeaderOffset = $stream.Position

        if ($machine -ne $ImageFileMachineAMD64) {
            throw ("{0} targets unsupported machine 0x{1:x4}; expected AMD64" -f $resolved, $machine)
        }
        if ($reader.ReadUInt16() -ne $ImageNtOptionalHdr64Magic) {
            throw "$resolved is not a PE32+ image"
        }
        if ($optionalHeaderSize -lt 0xd8) {
            throw "$resolved has a truncated PE32+ optional header"
        }

        $stream.Position = $optionalHeaderOffset + 0x46
        $dllCharacteristics = $reader.ReadUInt16()

        $stream.Position = $optionalHeaderOffset + 0x6c
        $directoryCount = $reader.ReadUInt32()
        if ($directoryCount -lt 13) {
            throw "$resolved does not declare an Import Address Table directory"
        }

        $stream.Position = $optionalHeaderOffset + 0x70 + (5 * 8)
        $relocationRva = $reader.ReadUInt32()
        $relocationSize = $reader.ReadUInt32()

        $stream.Position = $optionalHeaderOffset + 0x70 + (10 * 8)
        $loadConfigRva = $reader.ReadUInt32()
        $loadConfigSize = $reader.ReadUInt32()

        $stream.Position = $optionalHeaderOffset + 0x70 + (12 * 8)
        $iatRva = $reader.ReadUInt32()
        $iatSize = $reader.ReadUInt32()
        if ($iatRva -eq 0 -or $iatSize -eq 0) {
            throw "$resolved has no Import Address Table"
        }

        $sectionHeaderOffset = $optionalHeaderOffset + $optionalHeaderSize
        $iatSection = $null
        $rwxSections = [System.Collections.Generic.List[string]]::new()
        for ($i = 0; $i -lt $sectionCount; $i++) {
            $stream.Position = $sectionHeaderOffset + ($i * 40)
            $name = [System.Text.Encoding]::ASCII.GetString($reader.ReadBytes(8)).TrimEnd([char]0)
            $virtualSize = $reader.ReadUInt32()
            $virtualAddress = $reader.ReadUInt32()
            $rawSize = $reader.ReadUInt32()
            $stream.Position += 16
            $characteristics = $reader.ReadUInt32()

            $span = [Math]::Max([uint64]$virtualSize, [uint64]$rawSize)
            if ([uint64]$iatRva -ge [uint64]$virtualAddress -and [uint64]$iatRva -lt ([uint64]$virtualAddress + $span)) {
                $iatSection = [pscustomobject]@{ Name = $name; Characteristics = $characteristics }
            }
            if (($characteristics -band $ImageScnMemExecute) -ne 0 -and ($characteristics -band $ImageScnMemWrite) -ne 0) {
                $rwxSections.Add($name)
            }
        }

        if ($null -eq $iatSection) {
            throw ("{0} IAT RVA 0x{1:x8} is outside every section" -f $resolved, $iatRva)
        }

        [pscustomobject]@{
            Path = $resolved
            DllCharacteristics = $dllCharacteristics
            RelocationRva = $relocationRva
            RelocationSize = $relocationSize
            LoadConfigRva = $loadConfigRva
            LoadConfigSize = $loadConfigSize
            IATRva = $iatRva
            IATSize = $iatSize
            IATSectionName = $iatSection.Name
            IATSectionWritable = ($iatSection.Characteristics -band $ImageScnMemWrite) -ne 0
            RWXSections = $rwxSections
        }
    } finally {
        $reader.Dispose()
        $stream.Dispose()
    }
}

$failed = $false
foreach ($file in $Path) {
    try {
        $image = Read-PEImage -FilePath $file
        $missing = [System.Collections.Generic.List[string]]::new()
        if (($image.DllCharacteristics -band $ImageDllCharacteristicsHighEntropyVA) -eq 0) { $missing.Add('HIGH_ENTROPY_VA') }
        if (($image.DllCharacteristics -band $ImageDllCharacteristicsDynamicBase) -eq 0) { $missing.Add('DYNAMIC_BASE') }
        if (($image.DllCharacteristics -band $ImageDllCharacteristicsNXCompat) -eq 0) { $missing.Add('NX_COMPAT') }
        if ($image.RelocationRva -eq 0 -or $image.RelocationSize -eq 0) { $missing.Add('base relocations') }
        if ($image.IATSectionWritable) { $missing.Add("read-only IAT (currently in writable $($image.IATSectionName))") }
        if ($image.RWXSections.Count -gt 0) { $missing.Add("no RWX sections (found $($image.RWXSections -join ', '))") }

        $cfgEnabled =
            ($image.DllCharacteristics -band $ImageDllCharacteristicsGuardCF) -ne 0 -and
            $image.LoadConfigRva -ne 0 -and
            $image.LoadConfigSize -ne 0

        if ($missing.Count -gt 0) {
            $failed = $true
            Write-Error -ErrorAction Continue ("{0}: missing {1}" -f $image.Path, ($missing -join '; '))
            continue
        }

        $cfg = if ($cfgEnabled) { 'enabled' } else { 'unavailable in the Go compiler/linker (golang/go#35940)' }
        Write-Host ("PASS {0}: ASLR, DEP, read-only IAT ({1}), no RWX sections; CFG {2}" -f $image.Path, $image.IATSectionName, $cfg)
    } catch {
        $failed = $true
        Write-Error -ErrorAction Continue $_
    }
}

if ($failed) {
    exit 1
}
