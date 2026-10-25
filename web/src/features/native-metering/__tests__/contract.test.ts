import { test } from 'node:test'
import assert from 'node:assert/strict'
import { catalogSchema, priceSchema, preview, finalAmount, formatQuantity } from '../contract'
const price = { version: 'metered_v2', operation: 'speech', policy: 'unicode_scalar_v1', terms: [{ key: 'characters', unit: 'character', price: { numerator: '1', denominator: '1000' } }] } as const
test('character quantities and money serialize exactly beyond Number precision', () => {
 const parsed = priceSchema.parse(price)
 assert.equal(preview(parsed, { characters: { value: '1500', scale: 0 } }), '3/2')
 assert.equal(formatQuantity({ value: '9007199254740993', scale: 0 }), '9007199254740993')
 assert.equal(JSON.parse(JSON.stringify(parsed)).terms[0].price.denominator, '1000')
})
test('zero remains zero with minimum and step while pending has no final charge', () => {
 const parsed = priceSchema.parse({ ...price, terms: [{ ...price.terms[0], minimum: { value: '1000', scale: 0 }, step: { value: '1000', scale: 0 } }] })
 assert.equal(preview(parsed, { characters: { value: '0', scale: 0 } }), '0')
 assert.equal(preview(parsed, { characters: { value: '1001', scale: 0 } }), '2')
 assert.equal(finalAmount('measurement_pending', '0'), undefined)
 assert.equal(finalAmount('settled', '0'), '0')
})
test('invalid unit, duplicate tier, zero denominator and operation mismatch reject publication', () => {
 assert.equal(priceSchema.safeParse({ ...price, terms: [{ ...price.terms[0], unit: 'token' }] }).success, false)
 assert.equal(priceSchema.safeParse({ ...price, terms: [price.terms[0], price.terms[0]] }).success, false)
 assert.equal(priceSchema.safeParse({ ...price, terms: [{ ...price.terms[0], price: { numerator: '1', denominator: '0' } }] }).success, false)
 assert.equal(catalogSchema.safeParse({ model: { ocr: price } }).success, false)
})
