"""Independent verification: python verify_export.py EXPORT_DIR (requires pandas)."""
import sys
from pathlib import Path
import pandas as pd

root = Path(sys.argv[1])
for name in ("documents", "paragraphs", "codings"):
    dta = pd.read_stata(root / f"{name}.dta", convert_categoricals=False)
    csv = pd.read_csv(root / f"{name}.csv", dtype=str, keep_default_na=False)
    assert list(dta.columns) == list(csv.columns), name
    assert len(dta) == len(csv), name
    for column in dta:
        if dta[column].dtype == object:
            assert dta[column].tolist() == csv[column].tolist(), (name, column)
        else:
            expected = pd.to_numeric(csv[column].replace("", None))
            assert ((dta[column] == expected) | (dta[column].isna() & expected.isna())).all(), (name, column)
    print(f"{name}: {len(dta)} rows, all columns match")
