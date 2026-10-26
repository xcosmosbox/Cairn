#!/usr/bin/env python3
"""Adversarial provenance checks with invented, unrestricted source text."""
import copy
import hashlib
import tempfile
import unittest
from pathlib import Path

from inventory_span_check import public_exclusion_reason, validate


class SpanCheckTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        sources=[]
        for i in range(1,7):
            sid=f'S{i:02}'
            quote='If β, A may help B.'
            raw=quote.encode()
            (self.root/f'{sid}.md').write_bytes(raw)
            facet='Conditional, uncertain help from A to B.'
            pid=sid+'-P001'
            sources.append(dict(source_id=sid,original_sha256=hashlib.sha256(raw).hexdigest(),
                bytes=len(raw),lines=1,metadata={'published_at':'2020-01-01'},
                paragraphs=[dict(paragraph_id=pid,byte_start=0,byte_end=len(raw),line=1,
                    text=quote,preservation_facets=facet,publication_anchor='2020-01-01')],
                units=[dict(unit_id=sid+'-U001',paragraph_id=pid,byte_start=0,byte_end=len(raw),
                    source_quote=quote,expected_claim=quote,line_start=1,line_end=1,
                    disposition='substantive',exclusion_reason=None,preservation_facets=facet,
                    qualifiers={},classifications=['condition','uncertainty'],
                    assertion_scope='A conditional attributed assertion.',acceptance='Retain condition and uncertainty.')],
                explicit_relations=[dict(relation_id=sid+'-R001',paragraph_id=pid,
                    subject='A',predicate='may help if β',object='B',direction='subject -> object',
                    evidence_byte_start=0,evidence_byte_end=len(raw),evidence_quote=quote,
                    preservation_facets=facet,assertion_status='conditional source assertion',
                    required_representation='graph detail or supported explicit edge')]))
        self.inventory=dict(schema_version='v011-evidence-1',sources=sources)

    def test_valid_unicode_byte_spans(self):
        self.assertEqual(validate(self.inventory,self.root)['substantive_units'],6)

    def test_private_exclusion_text_never_becomes_public(self):
        self.assertEqual(public_exclusion_reason('navigation section label (Restricted Name)'), 'navigation')
        with self.assertRaisesRegex(ValueError,'unmapped'): public_exclusion_reason('Unknown restricted detail')

    def test_missing_source_and_changed_original_fail(self):
        bad=copy.deepcopy(self.inventory);bad['sources'].pop()
        with self.assertRaisesRegex(ValueError,'six ordered'):validate(bad,self.root)
        (self.root/'S01.md').write_text('Altered input')
        with self.assertRaisesRegex(ValueError,'hash mismatch'):validate(self.inventory,self.root)

    def test_unreviewed_suffix_and_overlap_fail(self):
        bad=copy.deepcopy(self.inventory);u=bad['sources'][0]['units'][0]
        u['byte_end']-=1;u['source_quote']=u['source_quote'][:-1];u['expected_claim']=u['source_quote']
        with self.assertRaisesRegex(ValueError,'uncovered'):validate(bad,self.root)
        bad=copy.deepcopy(self.inventory);u=copy.deepcopy(bad['sources'][0]['units'][0]);u['unit_id']='S01-U002';bad['sources'][0]['units'].append(u)
        with self.assertRaisesRegex(ValueError,'overlapping'):validate(bad,self.root)

    def test_detached_context_and_unexplained_exclusion_fail(self):
        bad=copy.deepcopy(self.inventory);bad['sources'][0]['units'][0]['preservation_facets']='A helps B always.'
        with self.assertRaisesRegex(ValueError,'detached facets'):validate(bad,self.root)
        bad=copy.deepcopy(self.inventory);bad['sources'][0]['units'][0]['disposition']='excluded'
        with self.assertRaisesRegex(ValueError,'unexplained exclusion'):validate(bad,self.root)

    def test_relation_credit_and_qualifier_drift_fail(self):
        bad=copy.deepcopy(self.inventory);bad['sources'][0]['explicit_relations'][0]['evidence_byte_start']=1
        with self.assertRaisesRegex(ValueError,'detached relation'):validate(bad,self.root)
        bad=copy.deepcopy(self.inventory);bad['sources'][0]['units'][0]['qualifiers']={'condition':[dict(text='if',byte_start=0,byte_end=2)]}
        with self.assertRaisesRegex(ValueError,'qualifier drift'):validate(bad,self.root)


if __name__=='__main__':unittest.main()
