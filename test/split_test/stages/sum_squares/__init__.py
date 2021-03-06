__MRO__ = """
stage SUM_SQUARES(
    in  float[] values,
    out float   sum,
) split using (
    in  float   value,
)
"""

def split(args):
    """Make a chunk for each value."""
    return {'chunks': [{'value': x, '__threads': 1, '__mem_gb': 1}
                       for x in args.values]}

def main(args, outs):
    outs.sum = args.value**2

def join(args, outs, chunk_defs, chunk_outs):
    #pylint: disable=unused-argument
    outs.sum = sum([out.sum for out in chunk_outs])
