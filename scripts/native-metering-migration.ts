// Offline only. bun scripts/native-metering-migration.ts input.json
// Input: { legacy: { model: "fixed request price" }, proposals: ModelNativePricing,
// examples: [{ model, operation, quantities: { dimension: {value,scale} } }] }
import { readFileSync } from 'node:fs'
import { catalogSchema, preview } from '../web/src/features/native-metering/contract'
const input = JSON.parse(readFileSync(process.argv[2], 'utf8')) as { legacy: Record<string, string>; proposals: unknown; examples: {model: string; operation: string; quantities: Record<string, {value: string; scale: number}>}[] }
const catalog = catalogSchema.parse(input.proposals)
console.log(JSON.stringify({ enabled: false, write_options: false, proposals: catalog, examples: input.examples.map((example) => ({ ...example, legacy_fixed_request_price: input.legacy[example.model] ?? null, new_exact_currency: preview(catalog[example.model][example.operation], example.quantities) })) }, null, 2))
