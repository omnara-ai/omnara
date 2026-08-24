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
    .transform((value) => value.normalize('NFC'))
    .refine((value) => Array.from(value).length <= 64, {
        message: 'Resource name cannot exceed 64 Unicode characters'
    })
    .refine((value) => !/^\\p{White_Space}|\\p{White_Space}$/u.test(value), {
        message: 'Resource name must not start or end with whitespace'
    })
    .refine((value) => !/[\\p{Cc}\\p{Cf}\\p{Cs}\\p{Default_Ignorable_Code_Point}\\u2800]/u.test(value), {
        message: 'Resource name contains an unsupported invisible, control, or format character'
    })
    .refine((value) => !value.includes('\\ufffd'), {
        message: 'Resource name contains the Unicode replacement character'
    })
    .refine((value) => !Array.from(value).some(
        (character) => character !== ' ' && /\\p{White_Space}/u.test(character)
    ), {
        message: 'Resource name may only use ordinary spaces'
    });`

if (generated.split(original).length !== 2) {
  throw new Error('expected exactly one generated ResourceName Zod schema')
}
const agentNameOriginal = 'export const zAgentName = z.string().max(64);'
const agentNameReplacement = replacement
  .replace('zResourceName', 'zAgentName')
  .replace('    .min(1)\n', '')
  .replaceAll('    .refine((value) => ', "    .refine((value) => value === '' || ")

if (generated.split(agentNameOriginal).length !== 2) {
  throw new Error('expected exactly one generated AgentName Zod schema')
}
writeFileSync(
  zodPath,
  generated.replace(agentNameOriginal, agentNameReplacement).replace(original, replacement),
)
