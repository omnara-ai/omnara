import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'

const outputPath = process.argv[2]
if (!outputPath) throw new Error('generated output path is required')

const zodPath = join(outputPath, 'zod.gen.ts')
const generated = readFileSync(zodPath, 'utf8')
const original = 'export const zResourceName = z.string().min(1).max(64);'
const replacement = `export const zResourceName = z
    .string()
    .min(1)
    .refine((value) => Array.from(value).length <= 64, {
        message: 'Resource name cannot exceed 64 Unicode characters'
    })
    .refine((value) => new TextEncoder().encode(value).length <= 256, {
        message: 'Resource name cannot exceed 256 UTF-8 bytes'
    })
    .refine((value) => !/^\\p{White_Space}|\\p{White_Space}$/u.test(value), {
        message: 'Resource name must not start or end with whitespace'
    })
    .refine((value) => !/[\\p{Cc}\\p{Cf}\\p{Cs}\\p{Default_Ignorable_Code_Point}\\u2800]/u.test(value), {
        message: 'Resource name contains an unsupported invisible, control, or format character'
    })
    .refine((value) => !Array.from(value).some(
        (character) => character !== ' ' && /\\p{White_Space}/u.test(character)
    ), {
        message: 'Resource name may only use ordinary spaces'
    });`

if (generated.split(original).length !== 2) {
  throw new Error('expected exactly one generated ResourceName Zod schema')
}
const defaultableOriginal = 'export const zDefaultableResourceName = z.string().max(64);'
const defaultableReplacement = replacement
  .replace('zResourceName', 'zDefaultableResourceName')
  .replace('    .min(1)\n', '')
  .replaceAll('(value) => ', "(value) => value === '' || ")

if (generated.split(defaultableOriginal).length !== 2) {
  throw new Error('expected exactly one generated DefaultableResourceName Zod schema')
}
writeFileSync(
  zodPath,
  generated.replace(defaultableOriginal, defaultableReplacement).replace(original, replacement),
)
