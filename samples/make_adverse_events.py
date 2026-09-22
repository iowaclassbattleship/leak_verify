#!/usr/bin/env python3
"""Writes a 2,000 row adverse-event extract shaped like the case data a
pharmacovigilance centre shares with a vendor or a researcher.

It is built to exercise every marking measure: a unique case identifier for
canary rows, fractional-second timestamps and four-decimal numbers for the
low-order-bit and noise carriers, and a reporter reference that reads as
personal text for the redaction carrier.
"""
import csv, random, datetime, pathlib

random.seed(20260922)

COUNTRY = ['SE', 'CH', 'DE', 'FR', 'NL', 'JP', 'US', 'BR', 'IN', 'AU']
AGE = ['0-1 month', '2 months-2 years', '3-11 years', '12-17 years',
       '18-44 years', '45-64 years', '65-74 years', '75+ years']
SEX = ['Male', 'Female', 'Unknown']
DRUG = ['atorvastatin', 'metformin', 'sertraline', 'amoxicillin', 'levothyroxine',
        'apixaban', 'omeprazole', 'salbutamol', 'methotrexate', 'lisinopril',
        'tramadol', 'quetiapine', 'ibuprofen', 'clopidogrel', 'insulin glargine']
REACTION = ['Nausea', 'Rash', 'Headache', 'Myocarditis', 'Hepatic enzyme increased',
            'Dizziness', 'Angioedema', 'Thrombocytopenia', 'Anaphylactic reaction',
            'Renal impairment', 'Pruritus', 'Fatigue', 'Bradycardia', 'Urticaria']
OUTCOME = ['Recovered', 'Recovering', 'Not recovered', 'Recovered with sequelae',
           'Fatal', 'Unknown']
SERIOUS = ['Serious', 'Non-serious']
QUALIFICATION = ['Physician', 'Pharmacist', 'Other health professional',
                 'Consumer', 'Lawyer']

rows = []
start = datetime.datetime(2023, 1, 1)
for i in range(2000):
    received = start + datetime.timedelta(
        days=random.randint(0, 900), seconds=random.randint(0, 86399),
        milliseconds=random.randint(0, 999))
    rows.append([
        f'WHO-{4100000 + i}',                              # unique identifier
        f'RPT-{random.randint(0, 999999):06d}-{random.choice("ABCDEFGH")}',
        random.choice(COUNTRY),
        random.choice(AGE),
        random.choice(SEX),
        random.choice(DRUG),
        random.choice(REACTION),
        random.choice(OUTCOME),
        random.choice(SERIOUS),
        random.choice(QUALIFICATION),
        received.strftime('%Y-%m-%d %H:%M:%S.') + f'{received.microsecond // 1000:03d}',
        f'{random.choice([5, 10, 20, 25, 40, 50, 75, 100, 200, 500]) * random.uniform(0.9, 1.1):.4f}',
        f'{random.expovariate(1 / 21):.4f}',
        f'{random.expovariate(1 / 9):.4f}',
    ])

out = pathlib.Path(__file__).with_name('adverse-events.csv')
with out.open('w', newline='') as f:
    w = csv.writer(f)
    w.writerow(['case_id', 'reporter_ref', 'country', 'age_group', 'sex', 'drug',
                'reaction', 'outcome', 'seriousness', 'reporter_qualification',
                'received_at', 'daily_dose_mg', 'treatment_days', 'onset_days'])
    w.writerows(rows)
print(out, len(rows), 'rows')
