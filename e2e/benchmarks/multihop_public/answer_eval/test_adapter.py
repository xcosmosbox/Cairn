import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from adapter import (OriginalBindings, SourceBindingError, construction_gate,
                     digest, match_expression, pack_context, check_answer)


def document(url='https://synthetic.test/a', body='alpha beta gamma'):
    return dict(title='Synthetic', author='Synthetic', source='Synthetic', published_at='2000-01-01', category='fixture', url=url, body=body)


class AdapterTests(unittest.TestCase):
    def test_exact_or_escaping_and_unicode_raw_clipping(self):
        self.assertEqual(match_expression('a "b"'), '"a" OR """b"""')
        binding = OriginalBindings([document(body='甲😀乙' * 200)])
        identity = next(iter(binding.documents))
        block = binding.verify_raw(dict(chunk_id='chunk', doc_id=identity, start=0, end=600, text='甲😀乙' * 200, metadata={'title': 'Synthetic'}))
        packed = pack_context([block], 600)
        shown = packed['evidence'][0]
        self.assertLessEqual(packed['visible_chars'], 600)
        self.assertEqual(shown['sources'][0]['end'], len(shown['text']))
        self.assertTrue(shown['text_truncated'])

    def test_no_hidden_source_or_relation_count_caps(self):
        block = {'id': 'node', 'text': 'Synthetic', 'metadata': {}, 'sources': [{'doc_id': str(i)} for i in range(12)],
                 'relations': [{'id': i} for i in range(25)], 'representation': 'generated_graph'}
        packed = pack_context([block])
        self.assertEqual(len(packed['evidence'][0]['sources']), 12)
        self.assertEqual(len(packed['evidence'][0]['relations']), 25)

    def test_graph_original_membership_and_unknown_hint_are_distinct(self):
        corpus = [document()]
        identity = digest(corpus[0]['url'])
        path = 'references/' + identity + '.md'
        proof = {'documents': [{'doc_id': identity, 'members': ['m']}],
                 'source_bindings': [{'node_uuid': 'node', 'member_id': 'm', 'file_path': path}]}
        binding = OriginalBindings(corpus, proof)
        source = {'path': path, 'member_id': 'm', 'start_line': 0, 'end_line': 0}
        self.assertEqual(binding.graph_source(source, 'node')['original_hint']['verification'], 'unknown_or_unverified')
        with self.assertRaises(SourceBindingError):
            binding.graph_source(source, 'different-uuid')

    def test_shared_member_ids_keep_document_incidence_denominator(self):
        docs = [{'doc_id': str(i), 'path': str(i) + '.md', 'members': ['shared', 'other'], 'materialized_members': ['shared', 'other']} for i in range(609)]
        proof = {'pass': True, 'errors': [], 'db_sha256': 'hash', 'documents': docs, 'source_bindings': [{'node_uuid': 'n-' + str(i), 'member_id': member, 'file_path': str(i) + '.md'} for i in range(609) for member in ('shared', 'other')]}
        value = construction_gate(proof, [str(i) for i in range(609)], 'hash')
        self.assertEqual(value['accepted_incidences'], 1218)
        proof['pass'] = False
        with self.assertRaises(SourceBindingError):
            construction_gate(proof, [str(i) for i in range(609)], 'hash')
        proof['pass'] = True
        docs[0]['materialized_members'] = []
        with self.assertRaises(SourceBindingError):
            construction_gate(proof, [str(i) for i in range(609)], 'hash')

    def test_invalid_citations_and_generated_quotes_fail(self):
        binding = OriginalBindings([document()])
        identity = next(iter(binding.documents))
        context = {'evidence': [{'id': 'e', 'text': 'alpha beta gamma', 'sources': [binding.original(identity)]}]}
        answer = {'answer': 'alpha', 'abstain': False, 'citations': ['e'], 'quotes': []}
        self.assertEqual(check_answer(answer, context, binding), answer)
        bad = copy.deepcopy(answer); bad['citations'] = ['unseen']
        with self.assertRaises(SourceBindingError):
            check_answer(bad, context, binding)
        bad = copy.deepcopy(answer); bad['quotes'] = [{'evidence_id': 'e', 'doc_id': identity, 'start': 1, 'end': 6, 'text': 'alpha'}]
        with self.assertRaises(SourceBindingError):
            check_answer(bad, context, binding)

    def test_structural_nodes_keep_rank_budget_without_source_backfill(self):
        binding = OriginalBindings([document()])
        blocks = [binding.graph_block({'id': str(i), 'body': 'Navigation text', 'label': label,
                                      'sources': [], 'edges': [{'description': 'Visible relation'}]})
                  for i, label in enumerate(('Skill', 'Domain', 'Subdomain') * 4)]
        packed = pack_context(blocks)
        self.assertEqual([row['id'] for row in packed['evidence']], [str(i) for i in range(10)])
        self.assertEqual(packed['visible_chars'], len(packed['serialized']))
        self.assertTrue(all(row['binding_status'] == 'structural_unbound' and row['sources'] == [] for row in packed['evidence']))
        self.assertIn('Visible relation', packed['serialized'])
        self.assertIn('structural_unbound', packed['serialized'])
        answer = {'answer': 'Navigation text', 'abstain': False, 'citations': ['0'], 'quotes': []}
        with self.assertRaises(SourceBindingError):
            check_answer(answer, packed, binding)
        for label in ('Entity', 'Concept', 'Unknown'):
            with self.assertRaises(SourceBindingError):
                binding.graph_block({'id': 'leaf', 'body': 'Unbound', 'label': label, 'sources': []})
        with self.assertRaises(SourceBindingError):
            binding.graph_block({'id': 'structure', 'body': 'Invalid ownership', 'label': 'Domain',
                                 'sources': [{'path': 'unknown', 'member_id': 'x'}]})
